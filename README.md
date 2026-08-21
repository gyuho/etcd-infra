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
and brings up one cluster per image with `aws up --binary-url --bastion` (a
presigned S3 URL with a verified SHA-256, plus an SSM-only bastion relay). The tests drive the members over SSM
RunCommand and systemd, arm failpoints through a systemd drop-in, and finish
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
member-to-member TCP 2379 and 2380; the bastion joins the same groups, so
bastion-to-member traffic needs no extra rules. Client traffic rides SSM
port-forwarding through the bastion, so no inbound rule for the test host is
required and etcd is never exposed publicly. The test host needs the AWS CLI
and session-manager-plugin.

Use least-privilege credentials: `hack/aws-e2e.iam-policy.json` is fully
portable — every ARN wildcards account and region, and no per-account
resource IDs appear, so the same file attaches unchanged in any AWS account.
Only one naming convention must pre-exist in the account: an
instance-profile role named `etcd-infra-ssm` with
`AmazonSSMManagedInstanceCore` attached. The S3 upload bucket needs no setup:
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

Setup in a fresh account (any region):

```bash
# instance-profile role the test instances run as (SSM-driven orchestration)
aws iam create-role --role-name etcd-infra-ssm --assume-role-policy-document '{
  "Version": "2012-10-17",
  "Statement": [{"Effect": "Allow",
    "Principal": {"Service": "ec2.amazonaws.com"},
    "Action": "sts:AssumeRole"}]
}'
aws iam attach-role-policy --role-name etcd-infra-ssm \
  --policy-arn arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore
aws iam attach-role-policy --role-name etcd-infra-ssm \
  --policy-arn arn:aws:iam::aws:policy/AmazonS3ReadOnlyAccess
aws iam create-instance-profile --instance-profile-name etcd-infra-ssm
aws iam add-role-to-instance-profile --instance-profile-name etcd-infra-ssm \
  --role-name etcd-infra-ssm

# the least-privilege user
aws iam create-user --user-name etcd-infra-aws-e2e
aws iam create-policy --policy-name etcd-infra-aws-e2e \
  --policy-document file://hack/aws-e2e.iam-policy.json
aws iam attach-user-policy --user-name etcd-infra-aws-e2e \
  --policy-arn arn:aws:iam::<account-id>:policy/etcd-infra-aws-e2e
aws iam create-access-key --user-name etcd-infra-aws-e2e
```

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

Pass `--bastion` to add a dedicated SSM-only relay instance (default
`t3a.nano`, or `t4g.nano` with `--arch arm64`; override with
`--bastion-instance-type`) in the same subnet and security groups. The AWS
e2e tests then reach member TCP 2379 over SSM port-forwarding through the
bastion instead of dialing instance IPs directly, so etcd needs no inbound
security-group rule from the test host — the production-realistic topology.
The relay only shuttles TCP streams, which is why it is sized far below the
members; it runs no test code.

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
If a run is interrupted mid-creation (for example SIGKILL between an instance
launch and the state write), an unrecorded instance can remain; the tag is the
source of truth — find and terminate orphans with
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
comma-separated KEY=VALUE variables in the etcd systemd unit.
