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
([gyuho/etcd@fix/snapdb-dir-fsync](https://github.com/gyuho/etcd/commits/fix/snapdb-dir-fsync))
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

## Client selection

Tests use the published etcd v3.7.1 client by default. Set `ETCD_INFRA_CLIENT=custom`
to use the temporary copy in `client/v3` with leader-aware mutation routing:

```bash
./hack/unit.sh
./hack/e2e.sh
ETCD_INFRA_CLIENT=custom ./hack/unit.sh
ETCD_INFRA_CLIENT=custom ./hack/e2e.sh
```

The custom module is a drop-in `go.etcd.io/etcd/client/v3` replacement. Its Go
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
`client/v3/leader_routing.go` documents each per-type decision.
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

The AWS compute manager implements `compute.Lifecycle.ReplaceMachine` for a
verified in-service ASG member, terminating it without decrementing desired
capacity and returning the ASG handle for replacement tracking. The ASG and its
launch-time bootstrap must restore the stable IP and EBS data volume. The
standalone `etcd-infra aws up` path does not create an ASG, so it intentionally does
not expose an `aws replace` command.
