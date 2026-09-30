# etcd-infra

Standalone etcd test infrastructure with an etcd installer, client health
checks, conformance scenarios, a stress/load generator, and a generic AWS EC2
provider. It has no Kubernetes packages or cluster lifecycle code.

## Build and install etcd

```bash
./hack/build.sh
./bin/etcd-infra install --version latest --dir ./bin
```

`install` resolves the latest official etcd release, verifies its published
SHA-256 checksum, and installs `etcd`, `etcdctl`, and `etcdutl`.

## Local container cluster

```bash
./bin/etcd-infra local up --members 3
./bin/etcd-infra local status
./bin/etcd-infra local replace --members 3 --member leader
./bin/etcd-infra conformance --scenario PUT_AND_GET_WITH_PREFIX
./bin/etcd-infra stress --scenario CONCURRENT_PUTS --duration 30 --workers 10 --rps 100
./bin/etcd-infra local down
```

Use `--members 1` for a single-member cluster. Client ports start at 2379 and
increment once per member; override the first one with `--port`.

`local replace` removes and recreates the selected container with the same
container-network IP and named data volume. Its default three-second downtime
forces a three-member cluster to elect a new leader; pass the same `--members`
and `--port` values used by `local up` when they are non-default.

Run the checked local replacement smoke test with `./hack/e2e.sh`.
Docker is used when available and running; otherwise, etcd-infra uses Podman.
Set `ETCD_INFRA_CONTAINER_RUNTIME=docker` or `podman` to select one explicitly.

`local up` also accepts `--image` to run a custom etcd image (for example a
fork build), `--extra-args` to append etcd server flags, `--env` for
comma-separated container environment variables, and `--aux-port` to publish
one extra container port per member as `containerPort:firstHostPort`:

```bash
./bin/etcd-infra local up --members 3 --image localhost/my-etcd:dev \
  --extra-args "--snapshot-count=10 --snapshot-catchup-entries=10" \
  --env "GOFAIL_HTTP=0.0.0.0:2234,GOFAIL_FAILPOINTS=raftBeforeSave=sleep(100)" \
  --aux-port 2234:33479
```

To keep experimental or private-branch flags out of shell history, scripts,
and git, pass them in a file instead: `--extra-args-file` (default
`$ETCD_INFRA_EXTRA_ARGS_FILE`) reads one argument per line, ignores blank
lines and `#` comments, and passes each line verbatim — values with spaces
need no quoting. File entries are appended before `--extra-args`, so a
repeated inline flag overrides the file entry:

```bash
./bin/etcd-infra local up --image localhost/my-etcd:dev \
  --extra-args-file ~/private/etcd-flags
```

## Snapshot durability E2E (snap.db dir fsync)

`./hack/snapdb-e2e.sh` validates the snap.db directory-fsync fix
([gyuho/etcd@test](https://github.com/gyuho/etcd/commits/test))
end to end with real binaries, containers, and volumes. It builds
gofail-enabled images from the fix commit and its unfixed parent
(`hack/snapdb/build.sh`), then runs three tests
(`cmd/etcd-infra/local_snapdb_e2e_test.go`):

- **Crash window**: a member SIGKILLed between the snap.db rename and
  SaveDBFrom's return boots cleanly and catches up via resend — the WAL
  snapshot record is only written after SaveDBFrom returns, so the crash
  cannot leave a durable record pointing at an unconfirmed snap.db.
- **Loud fsync failure**: an injected snap-directory fsync error surfaces in
  the member's logs ("failed to save incoming database snapshot") instead of
  silently acknowledging an undurable snapshot, and the member recovers once
  the leader resends.
- **Blast radius**: fabricating the post-crash state the fix makes
  unreachable (durable WAL snapshot record, snap.db directory entry deleted
  from the volume — what a machine crash does to an un-fsynced rename) makes
  the member panic loudly on boot with "failed to find database snapshot
  file", and the documented remediation (wipe and re-add the member) restores
  the cluster. Runs on the fixed and unfixed images alike, since no local
  environment can drop the page cache on demand; once the entry is lost, no
  fsync can bring it back.

Failpoints are armed through `GOFAIL_FAILPOINTS` at process boot so they
cannot race the leader's snapshot stream.

### AWS power-loss validation

`./hack/aws-snapdb-e2e.sh` runs the same suite on EC2 and adds the one test
no local setup can do: a real machine crash. It builds gofail-enabled
linux/amd64 binaries from the fix and control commits, uploads them to S3,
and brings up one cluster per image with `aws up --binary-url` (a
presigned S3 URL with a verified SHA-256, plus one stress client instance).
The compiled test binary then ships to the stress client through S3 and runs
there, inside the VPC (`etcd-infra aws drive`). The tests drive the members
over SSM RunCommand and systemd, arm failpoints through a systemd drop-in,
and finish
with `TestSnapDBHardPowerLossAWSE2E`: with a snapshot received and the WAL
record durable, the member is hard-rebooted in-guest with
`echo b > /proc/sysrq-trigger` — the SysRq reboot(b) command from the EC2
documentation, issued over SSM because the serial console is not automatable.
The guest drops its page cache, so only fsynced data survives on EBS; on the
fixed build the member must boot from the snap.db and rejoin.

For the back-to-back discriminator, `TestSnapDBHardPowerLossNoJournal*AWSE2E`
reinstalls the target member with its data directory on a loop-mounted ext2
filesystem (no journal, so the WAL fsync cannot commit the rename's metadata
as a side effect) before the hard crash: the control build must panic with
the field-report signature, and the fixed build must boot from the snap.db.
The fstab entry restores the mount at boot before the etcd unit starts.

Required environment: `AWS_REGION`, `ETCD_INFRA_AWS_VPC`,
`ETCD_INFRA_AWS_AMI`, and `ETCD_INFRA_AWS_INSTANCE_PROFILE`; optionally
`ETCD_INFRA_AWS_SUBNET`, `ETCD_INFRA_AWS_SECURITY_GROUPS`, and
`ETCD_INFRA_AWS_S3_BUCKET` (default derived from account, region, and
month). The security groups must allow
member-to-member TCP 2379 and 2380; the stress clients join the same groups,
so client-to-member traffic needs no extra rules. All test traffic executes
inside the VPC on the stress clients, so no inbound rule for the test host
is required and etcd is never exposed publicly. The test host needs only the
AWS CLI.

Use least-privilege credentials: `hack/aws-e2e.iam-policy.json` is fully
portable — every ARN wildcards account and region, and no per-account
resource IDs appear, so the same file attaches unchanged in any AWS account.
Only one naming convention must pre-exist in the account: an
instance-profile role named `etcd-infra-ssm` (created by `etcd-infra aws iam
create-user`, below). The S3 upload bucket needs no setup:
`hack/aws-snapdb-e2e.sh` derives the name as
`etcd-infra-e2e-<account>-<region>-v0-<YYYYMM>` — deterministic within a
month, rotated by name — and creates it with public access blocked on first
use (set `ETCD_INFRA_AWS_S3_BUCKET` to override). The blast radius is bounded by
tag gates, not pinned resource IDs: instances must carry the
`etcd-infra.cluster` tag at creation, and only tagged instances and volumes
can be terminated, deleted, attached, or driven over SSM. The tag gates check
key presence with any value, so the user can also drive another engineer's
etcd-infra cluster in the same account; everything without the tag, EKS
included, is unreachable.

### IAM setup: `etcd-infra aws iam`

One command, run once with administrator credentials, creates the
least-privilege user every other command should run as, attaches the policy,
and sets up the instance role (any region; IAM is global):

```bash
etcd-infra aws iam create-user                    # plan only (default --dry-run)
etcd-infra aws iam create-user --dry-run=false --access-key
aws configure set aws_access_key_id <access_key_id> --profile etcd-infra-aws-e2e
aws configure set aws_secret_access_key <secret_access_key> --profile etcd-infra-aws-e2e
export AWS_PROFILE=etcd-infra-aws-e2e ETCD_INFRA_AWS_INSTANCE_PROFILE=etcd-infra-ssm

etcd-infra aws iam status                         # key=value; ready=true when converged
etcd-infra aws iam delete-user [--role]           # admin credentials again
```

`create-user` is idempotent and applies the reviewed files embedded from
`hack/`, so editing a policy file and rerunning `create-user` is how a policy
change rolls out (it adds a new default policy version and prunes the oldest
once IAM's five-version limit is reached; `status` reports `current=false`
for a stale policy). It converges:

- policy `etcd-infra-aws-e2e` = `hack/aws-e2e.iam-policy.json`;
- user `etcd-infra-aws-e2e` (`--user` for another name) with that policy
  attached **and set as its permissions boundary**, so the user can never
  exceed the policy even if something broader is attached later;
- with `--role` (default): role and instance profile `etcd-infra-ssm` (EC2
  trust) with `AmazonSSMManagedInstanceCore`, `AmazonS3ReadOnlyAccess`, and
  policy `etcd-infra-ssm-exec` = `hack/aws-ssm-role-exec.iam-policy.json`
  (the suites execute on the stress clients, so the role carries the same
  tag-scoped execution permissions). The name is fixed: the user policy only
  allows `iam:PassRole` on `role/etcd-infra-ssm`;
- with `--access-key`: a new access key, printed once on stdout (IAM allows
  two per user).

Everything `create-user` creates is tagged
`etcd-infra.managed-by=etcd-infra-aws-iam`, and `delete-user` deletes only
tagged resources: the user (with its access keys, console password, MFA
devices, and other credentials) and its policy, plus with `--role` the
instance profile, role, and exec policy (running etcd-infra instances then
lose SSM, so the default keeps them). Resources that already existed without
the tag, e.g. made by hand, are adopted by `create-user` (policies attached,
documents updated, boundary set if it had none) but never deleted; a policy
still attached elsewhere, or a role still in an untagged instance profile, is
kept. `create-user` refuses to run as the target user itself, to replace a
different boundary an adopted user already has (it may be stricter), and to
run outside the `aws` partition (the policies use `arn:aws:` ARNs).

The AWS CLI region must match the bucket's region for uploads and presigned
URLs; the scripts already require `AWS_REGION`.

## Client selection

Tests use the published etcd v3.7.1 client by default. Set `ETCD_INFRA_CLIENT=custom`
to import the leader-aware client from the fork
([`gyuho/etcd`](https://github.com/gyuho/etcd)) — `go.custom.mod` replaces
`go.etcd.io/etcd/client/v3` with `github.com/gyuho/etcd/client/v3` at a
pinned commit of the client-only slice, and tracks etcd main pseudo-versions
for `api/v3` and `client/pkg/v3` (the fork's client needs the Masterminds
semver migration, which the published v3.8.0-alpha.0 tag predates). The full
stack — response-driven leader hints plus the snap.db fix — lives on branch
[`test`](https://github.com/gyuho/etcd/commits/test/); its client couples to
the branch's own `api` module, which Go module rules cannot replace (declared
paths carry /v3, fork directory paths do not), so the go.mod import uses the
client-only commit, and the server binaries are built from branch `test`
in-repo by `hack/snapdb/build.sh`:

```bash
./hack/unit.sh
./hack/e2e.sh
ETCD_INFRA_CLIENT=custom ./hack/unit.sh
ETCD_INFRA_CLIENT=custom ./hack/e2e.sh
```

The fork module is a drop-in `go.etcd.io/etcd/client/v3` replacement. Its Go
API uses `clientv3.DefaultBalancerName` (`round_robin`) unless it is overwritten
with the namespaced `clientv3.LeaderAwareBalancerName` (`etcd_leader_aware`):

```go
cfg = cfg.WithBalancer(clientv3.LeaderAwareBalancerName)
client, err := clientv3.New(cfg)
```

The refresh interval, rediscovery delay, and Status timeout default to 30, 3,
and 5 seconds. Periodic refreshes include jitter. `Unavailable`,
`DeadlineExceeded`, and `NotLeader` responses clear the hint and schedule a
prompt, jittered rediscovery attempt.
The opt-in policy delegates reads and unknown or unavailable leaders to
grpc-go's native `round_robin` policy. The leader-aware requests are the consensus
writes: KV mutations (including mutating transaction branches), lease grant
and revoke, auth administration including `Authenticate`, and `Alarm`.
`MoveLeader` also uses leader-aware routing because only the leader serves it; a follower
rejects it with `ErrNotLeader`. Reads, watches, lease keep-alives, and
member-local maintenance stay on `round_robin`; `isMutationRequest` in
`client/v3/leader_routing.go` in the fork documents each per-type decision.
Applications can tune the freshness/load trade-off when needed:

```go
cfg = cfg.
	WithLeaderAwareRefreshInterval(15 * time.Second).
	WithLeaderAwareRediscoveryDelay(time.Second).
	WithLeaderAwareStatusTimeout(3 * time.Second)
```

## Existing etcd cluster

Both runners accept the same endpoint and TLS flags:

```bash
./bin/etcd-infra conformance \
  --endpoints https://host1:2379,https://host2:2379 \
  --ca-cert ca.crt --client-cert client.crt --client-key client.key

./bin/etcd-infra stress \
  --endpoints https://host1:2379,https://host2:2379 \
  --ca-cert ca.crt --client-cert client.crt --client-key client.key
```

Omit `--scenario` to run every copied scenario.

## AWS cluster

AWS mode uses an existing VPC, subnet/security groups, Linux AMI, and IAM
instance profile. It does not create network or IAM infrastructure. The AMI
must provide systemd, curl, tar, sha256sum, and a running SSM agent. Security
groups must allow member-to-member TCP 2379 and 2380.

`aws up` always creates stress client instances (`--stress-clients N`,
default 1; `t3a.medium`, or `t4g.medium` with `--arch arm64`; override
with `--bastion-instance-type`) in the members' security groups. Suites run
on them: `etcd-infra aws drive` ships the test or suite binary through S3,
executes it on each client over SSM RunCommand against the VPC endpoints, and
collects the results from S3. With N > 1 the clients spread round-robin over
the VPC's subnets, so stress load is balanced across availability zones. etcd
never needs a public ingress rule, and there is no port-forwarding anywhere.

Preview is the default and makes no AWS changes:

```bash
./bin/etcd-infra aws up \
  --region us-west-2 \
  --vpc vpc-123 \
  --subnet subnet-123 \
  --security-groups sg-123 \
  --ami ami-123 \
  --instance-profile etcd-infra-ssm
```

Create only after reviewing the plan:

```bash
./bin/etcd-infra aws up ... --dry-run=false
./bin/etcd-infra aws status
./bin/etcd-infra aws down
```

AWS state is stored under `~/.etcd-infra/aws/`. Created clusters use plain HTTP
inside the supplied VPC and are intended only for isolated test infrastructure.
Teardown deletes only the resources the tool created and recorded in the state
file: instances and data volumes go by their recorded IDs, never by tag sweep.
If a run is interrupted mid-creation (for example SIGKILL between an instance
launch and the state write), an unrecorded instance can remain; find and
terminate orphans by hand with
`aws ec2 describe-instances --filters Name=tag:etcd-infra.cluster,Values=<name>`.

The AWS compute manager implements `compute.Lifecycle.ReplaceMachine` two
ways. For a verified in-service ASG member it terminates the instance without
decrementing desired capacity and returns the ASG handle for replacement
tracking; the ASG and its launch-time bootstrap must restore the stable IP
and EBS data volume. For a standalone instance created with `--replaceable`
it captures the launch spec, terminates the instance, relaunches it with the
same private IP and tags, and reattaches the member's dedicated data volume
(`DeleteOnTermination=false`), so the member keeps its identity and its data
dir:

```bash
./bin/etcd-infra aws up ... --replaceable
./bin/etcd-infra aws replace --name my-cluster --member leader   # or a member name
```

`aws replace` resolves "leader" over client endpoints (bastion tunnels when
the cluster has one), then re-bootstraps the replacement with the recorded
release version, extra args, and environment. Clusters created with
`--binary-url` cannot be replaced: the presigned URL expires.
`hack/aws-conformance-stress-e2e.sh` runs a replace between its two
conformance passes when `ETCD_INFRA_AWS_REPLACE_MEMBER` is set (member name
or "leader"), mirroring `hack/e2e.sh`. `aws down` deletes the tagged data
volumes.

`./hack/aws-replace-e2e.sh` is the AWS counterpart of the local replacement
E2E tests: `TestAWSReplaceLeaderHandoffAWSE2E` replaces the leader's machine
and asserts a new leader is elected during the outage, and
`TestAWSReplaceFollowerAWSE2E` replaces a follower while the cluster keeps
serving. Both assert the replacement keeps the member's name, private IP, and
data. Required environment matches the conformance/stress script.

`aws tunnel --name <cluster>` opens one SSM port-forwarding session per
member through the bastion, prints the loopback client endpoints as one CSV
line on stdout, and holds the sessions until interrupted (progress goes to
stderr). Conformance and stress runs against a bastion cluster go through it.

`./hack/aws-conformance-stress-e2e.sh` wraps the whole flow: build, `aws up
--bastion`, tunnels, the conformance suite, the stress suite, teardown.
Required environment: `AWS_REGION`, `ETCD_INFRA_AWS_VPC`,
`ETCD_INFRA_AWS_AMI`, and `ETCD_INFRA_AWS_INSTANCE_PROFILE`; optional
scenario and stress-tuning overrides are listed in the script header.

`aws up` also accepts `--binary-url` with `--binary-sha256` to install a
custom etcd binary (for example a gofail-enabled fork build) instead of a
release tarball, `--extra-args` to append etcd server flags, and `--env` for
comma-separated KEY=VALUE variables in the etcd systemd unit. Like `local
up`, it accepts `--extra-args-file` (default `$ETCD_INFRA_EXTRA_ARGS_FILE`)
to read flags from a file, one per line, keeping private-branch flags out of
shell history and scripts; the resolved flags are recorded in the local state
file (mode 0600) so `aws replace` can reproduce a member exactly.

## AWS dev group (ephemeral identical Ubuntu instances)

`aws dev` manages a group of empty, identical Ubuntu instances for manual or
agent-driven testing of a private branch: no etcd, a mounted EBS data volume
per box, the AWS CLI with a verified S3 results prefix, and SSM command
access. A group is one EC2 **launch template** (the pinned spec) and one
**Auto Scaling group** launching `--count` copies of it, per AWS's current
recommendation (launch templates, not launch configurations). It reuses the
`etcd-infra-ssm` instance profile and the state store
(`~/.etcd-infra/aws/<name>.json`).

```bash
# Preview (default), then create. The AMI defaults to the latest Canonical
# Ubuntu Server 24.04 for --arch, resolved from Canonical's public SSM
# parameter and pinned into the template; override with --ubuntu-release
# (22.04, 24.04, 26.04) or --ami.
./bin/etcd-infra aws dev up --name dev01 --count 3 \
  --instance-profile etcd-infra-ssm --bucket etcd-infra-e2e-<account>-<region>-v0-<YYYYMM>
./bin/etcd-infra aws dev up ... --dry-run=false

./bin/etcd-infra aws dev run --name dev01 -- uname -a            # every box, in parallel
./bin/etcd-infra aws dev run --name dev01 --instance i-0abc --script ./t.sh
./bin/etcd-infra aws dev scale --name dev01 --count 5            # same spec, more boxes
./bin/etcd-infra aws dev status --name dev01
./bin/etcd-infra aws dev down --name dev01
aws ssm start-session --target <instance-id>                     # interactive shell
```

Networking: groups only launch into an **existing** VPC and never create,
modify, or delete network resources, so any number of groups (different
`--name`s) can share one VPC. `--vpc` defaults to `$ETCD_INFRA_AWS_VPC`, then
the region's default VPC; `--subnets` defaults to every subnet in the VPC
(the group spreads boxes across their availability zones); security groups
default to the VPC's `default` group (no inbound rules are needed). Every
subnet needs outbound internet or NAT for the first-boot installs.

The spec, identical for every box including later scale-outs (the group
uses template version 1 explicitly, never `$Latest`):

- `--instance-type` (default t3a.medium, or t4g.medium with `--arch arm64`),
  the pinned AMI, IMDSv2 required (hop limit 2 so containers can use it).
- An encrypted gp3 data volume (`--volume-size-gb`, default 32) that EC2
  deletes with its instance, formatted and mounted at `--mount-point`
  (default `/mnt/data`; allowed: `/data`, `/var/lib/etcd`, or under `/mnt/`,
  `/data/`, `/srv/`, so it can never hide OS state) with an fstab entry, so it
  survives in-guest reboots. Only the EBS NVMe disk is selected;
  instance-store disks are never formatted.
- First-boot setup in the template's user data (log:
  `/var/log/etcd-infra-dev-setup.log`): the SSM agent if the AMI lacks it,
  the mount, the AWS CLI, and a probe write of `<results>.dev-ready` proving
  the box's results prefix `s3://<bucket>/etcd-infra/dev/<name>/<instance-id>/`
  is writable (covered by the stock `etcd-infra-ssm-exec` role policy for
  `etcd-infra-e2e-*` buckets).

`up` and `scale` return once the group has exactly `--count` in-service
boxes and each one finished setup (on failure they show the setup log's
tail). `--count` is 1-20 for `up` and 0-20 for `scale`; on scale-in, Auto
Scaling picks which boxes to terminate. The group has `AZRebalance` and
`ReplaceUnhealthy` suspended, so it never terminates or replaces a box on its
own. stdout of `up`, `scale`, and `status` is only stable `key=value` lines
(`account_id`, `vpc_id`, `auto_scaling_group`, `launch_template_id`, `count`,
`results_uri`, `state_file`, ..., plus one `instance=<id> lifecycle=...
health=... az=... private_ipv4=... results_uri=...` line per box); progress
and suggested next commands go to stderr.

`aws dev run` executes as root over SSM RunCommand on every in-service box
(or the `--instance` list) in parallel, from the mount point, with
`/etc/profile.d/etcd-infra-dev.sh` sourced (login shells from
`start-session` source it too): `ETCD_INFRA_DEV_NAME`,
`ETCD_INFRA_DEV_INSTANCE_ID`, `ETCD_INFRA_DEV_DIR` (the mount point),
`ETCD_INFRA_DEV_RESULTS` (the box's S3 prefix), and `AWS_REGION`. It prints
output after the command finishes, preceded by `=== <instance-id> exit=<code>
===` when several boxes ran; any non-zero remote exit makes it exit 1 naming
the failed boxes. `--timeout` (default 1h) bounds the run. SSM caps captured
output at 24,000 characters per stream, so write large output under
`$ETCD_INFRA_DEV_DIR` and upload it: `aws s3 cp --recursive out/
"$ETCD_INFRA_DEV_RESULTS"out/`. Commands that mention or run
`reboot`/`shutdown` run in the guest; they never terminate a box. A reboot
keeps everything (the volume remounts); a poweroff/halt only *stops* the box,
which keeps billing for its volumes until `dev down`. To ship a locally built
binary, `aws s3 cp` it under the group's results prefix from the host, then
`aws s3 cp` it down in a `dev run`.

Cleanup: the template and group are named after `--name` and, together with
a random owner token, recorded in the state file before either is created,
so a failed or interrupted `up` is always cleanable (its error names the `dev
down` command). Everything the group creates is tagged
`etcd-infra.cluster=<name>`, `etcd-infra.role=dev`, and
`etcd-infra.dev-owner=<token>`. `dev down` (also reached via `aws down --name
<name>`):

1. refuses to run unless the credentials are for the account recorded at
   `up` (another account would see "not found" for live resources);
2. force-deletes the Auto Scaling group, which terminates its boxes, and
   waits until the group is gone;
3. terminates any instance still carrying the group's three tags and waits
   until every one is terminated, which is when EC2 deletes their data
   volumes;
4. deletes the launch template (by ID), then removes the state file.

Steps 2 and 4 act only on resources carrying this state's owner token: a
name alone is not proof of ownership, so another user's same-named group is
never touched. Any failure keeps the state file, so rerunning `dev down`
finishes the job. It is idempotent, refuses etcd-cluster state, and keeps the
S3 results. `up` refuses a `--name` (3+ characters) whose template or group
already exists without its owner token, and leaves that group untouched.
State left by the earlier single-instance `aws dev` is still torn down by
`dev down` (terminate and wait); other commands ask you to recreate it.

IAM (`hack/aws-e2e.iam-policy.json`; after upgrading, rerun
`etcd-infra aws iam create-user --dry-run=false` to roll it out): launch
templates and Auto Scaling groups may only be created with, and managed
when carrying, the `etcd-infra.cluster` tag; groups must pin a template
version (`autoscaling:LaunchTemplateVersionSpecified`);
`AutoScalingServiceLinkedRole` lets the first group in an account create
`AWSServiceRoleForAutoScaling`. `SSMReadUbuntuAMIParameters` grants the AMI
lookup (not needed with `--ami`), and `SSMStartSessionShellDocument` grants
interactive shells (which also need the Session Manager plugin on the host).
