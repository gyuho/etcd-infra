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
./bin/etcd-infra conformance --scenario PUT_AND_GET_WITH_PREFIX
./bin/etcd-infra stress --scenario CONCURRENT_PUTS --duration 30 --workers 10 --rps 100
./bin/etcd-infra local down
```

Use `--members 1` for a single-member cluster. Client ports start at 2379 and
increment once per member; override the first one with `--port`.

Run the checked local smoke test with `./hack/e2e.sh`.
Docker is used when available and running; otherwise, etcd-infra uses Podman.
Set `ETCD_INFRA_CONTAINER_RUNTIME=docker` or `podman` to select one explicitly.

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
