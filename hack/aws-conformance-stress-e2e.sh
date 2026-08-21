#!/usr/bin/env bash
# Runs the etcd conformance and stress suites against an AWS EC2 cluster:
# brings up one release-image cluster with "etcd-infra aws up --bastion",
# opens SSM port-forwarding tunnels through the bastion ("etcd-infra aws
# tunnel"), runs the suites against the loopback endpoints, and tears the
# cluster down. Client connections originate at the bastion inside the VPC,
# so etcd never needs a public ingress rule — mirroring production.
#
# Required environment:
#   AWS_REGION (or AWS default region)
#   ETCD_INFRA_AWS_VPC               existing VPC ID
#   ETCD_INFRA_AWS_AMI               Linux AMI with systemd, curl, tar,
#                                    sha256sum, and a running SSM agent
#   ETCD_INFRA_AWS_INSTANCE_PROFILE  IAM instance profile with SSM permissions
#
# Optional:
#   ETCD_INFRA_AWS_SUBNET            existing subnet ID
#   ETCD_INFRA_AWS_SECURITY_GROUPS   comma-separated security group IDs; must
#                                    allow member-to-member TCP 2379 and 2380
#                                    (the bastion shares them)
#   ETCD_INFRA_AWS_VERSION           etcd release version (default: latest)
#   ETCD_INFRA_AWS_CONFORMANCE_SCENARIO  run one scenario (default: all)
#   ETCD_INFRA_AWS_STRESS_SCENARIO   run one scenario (default: all)
#   ETCD_INFRA_AWS_STRESS_DURATION   seconds per stress scenario (default: 60)
#   ETCD_INFRA_AWS_STRESS_WORKERS    concurrent workers (default: 10)
#   ETCD_INFRA_AWS_STRESS_RPS        requests per second (default: 100)
#   ETCD_INFRA_SLOW_PATH_MULTIPLIER  latency-budget multiplier for the
#                                    bastion-tunnel path (default: 2)
#   ETCD_INFRA_CLIENT                "custom" builds the leader-aware client
#                                    from the fork (default: official)
#   ETCD_INFRA_AWS_REPLACE_MEMBER    when set (member name or "leader"), create
#                                    the cluster with --replaceable and replace
#                                    this member between the two conformance
#                                    passes, mirroring hack/e2e.sh
#
# The test host needs the AWS CLI and session-manager-plugin in PATH.
# Credentials: use the least-privilege user from hack/aws-e2e.iam-policy.json
# so the suite cannot touch EKS or any other AWS resources.
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cluster="etcd-infra-aws-conformance-stress"

for var in ETCD_INFRA_AWS_VPC ETCD_INFRA_AWS_AMI ETCD_INFRA_AWS_INSTANCE_PROFILE; do
    if [[ -z "${!var:-}" ]]; then
        echo "${var} is required" >&2
        exit 2
    fi
done
command -v aws >/dev/null 2>&1 || { echo "the AWS CLI is required" >&2; exit 2; }
command -v session-manager-plugin >/dev/null 2>&1 || { echo "session-manager-plugin is required: the suites reach the members over SSM port-forwarding through the bastion" >&2; exit 2; }

tmpdir="$(mktemp -d)"
tunnel_pid=""
cleanup() {
    if [[ -n "${tunnel_pid}" ]]; then
        kill "${tunnel_pid}" >/dev/null 2>&1 || true
    fi
    "${project_root}/bin/etcd-infra" aws down --name "${cluster}" || echo "WARN: aws down failed for ${cluster}; cluster may leak — check ~/.etcd-infra/aws/ and EC2" >&2
    rm -rf "${tmpdir}"
}
trap cleanup EXIT

"${project_root}/hack/build.sh"
# Remove any stale cluster with the same name without tearing down tmpdir.
"${project_root}/bin/etcd-infra" aws down --name "${cluster}" >/dev/null 2>&1 || true

up_args=(
    --name "${cluster}" --members 3 --bastion
    --vpc "${ETCD_INFRA_AWS_VPC}"
    --ami "${ETCD_INFRA_AWS_AMI}"
    --instance-profile "${ETCD_INFRA_AWS_INSTANCE_PROFILE}"
    --version "${ETCD_INFRA_AWS_VERSION:-latest}"
    --dry-run=false
)
if [[ -n "${ETCD_INFRA_AWS_REPLACE_MEMBER:-}" ]]; then
    up_args+=(--replaceable)
fi
if [[ -n "${AWS_REGION:-}" ]]; then
    up_args+=(--region "${AWS_REGION}")
fi
if [[ -n "${ETCD_INFRA_AWS_SUBNET:-}" ]]; then
    up_args+=(--subnet "${ETCD_INFRA_AWS_SUBNET}")
fi
if [[ -n "${ETCD_INFRA_AWS_SECURITY_GROUPS:-}" ]]; then
    up_args+=(--security-groups "${ETCD_INFRA_AWS_SECURITY_GROUPS}")
fi
"${project_root}/bin/etcd-infra" aws up "${up_args[@]}"

# "aws tunnel" prints one stdout line with the CSV of loopback endpoints once
# every tunnel accepts connections, then holds the sessions in the
# foreground. Progress goes to stderr.
"${project_root}/bin/etcd-infra" aws tunnel --name "${cluster}"     > "${tmpdir}/endpoints" 2> "${tmpdir}/tunnel.log" &
tunnel_pid=$!
for _ in $(seq 1 90); do
    [[ -s "${tmpdir}/endpoints" ]] && break
    if ! kill -0 "${tunnel_pid}" 2>/dev/null; then
        echo "aws tunnel exited before the endpoints were ready:" >&2
        cat "${tmpdir}/tunnel.log" >&2
        exit 1
    fi
    sleep 1
done
endpoints="$(cat "${tmpdir}/endpoints")"
if [[ -z "${endpoints}" ]]; then
    echo "aws tunnel never printed endpoints; log:" >&2
    cat "${tmpdir}/tunnel.log" >&2
    exit 1
fi
echo "cluster endpoints via bastion: ${endpoints}"

conformance_args=(--endpoints "${endpoints}")
if [[ -n "${ETCD_INFRA_AWS_CONFORMANCE_SCENARIO:-}" ]]; then
    conformance_args+=(--scenario "${ETCD_INFRA_AWS_CONFORMANCE_SCENARIO}")
fi
"${project_root}/bin/etcd-infra" conformance "${conformance_args[@]}"

if [[ -n "${ETCD_INFRA_AWS_REPLACE_MEMBER:-}" ]]; then
    "${project_root}/bin/etcd-infra" aws replace --name "${cluster}" \
        --member "${ETCD_INFRA_AWS_REPLACE_MEMBER}"
    "${project_root}/bin/etcd-infra" conformance "${conformance_args[@]}"
fi

# Snapshot peer-byte counters around the stress run: the sent-bytes delta is
# the peer bandwidth the client routing consumed, so an official-client run
# and a leader-aware run (ETCD_INFRA_CLIENT=custom hack/build.sh) can be
# compared directly.
"${project_root}/bin/etcd-infra" metrics --endpoints "${endpoints}" | tee "${tmpdir}/metrics-before.txt"

stress_args=(
    --endpoints "${endpoints}"
    --duration "${ETCD_INFRA_AWS_STRESS_DURATION:-60}"
    --workers "${ETCD_INFRA_AWS_STRESS_WORKERS:-10}"
    --rps "${ETCD_INFRA_AWS_STRESS_RPS:-100}"
)
if [[ -n "${ETCD_INFRA_AWS_STRESS_SCENARIO:-}" ]]; then
    stress_args+=(--scenario "${ETCD_INFRA_AWS_STRESS_SCENARIO}")
fi
# SSM port-forwarding adds ~150ms/RTT plus proxy jitter; under load, marginal
# p99 measurements land just over thresholds tuned for direct/VPN links. The
# multiplier scales those thresholds only (success rates stay strict).
ETCD_INFRA_SLOW_PATH_MULTIPLIER="${ETCD_INFRA_SLOW_PATH_MULTIPLIER:-2}" \
    "${project_root}/bin/etcd-infra" stress "${stress_args[@]}"

"${project_root}/bin/etcd-infra" metrics --endpoints "${endpoints}" | tee "${tmpdir}/metrics-after.txt"
before_sent=$(awk '/^TOTAL/ {print $2}' "${tmpdir}/metrics-before.txt")
after_sent=$(awk '/^TOTAL/ {print $2}' "${tmpdir}/metrics-after.txt")
echo "peer-sent bytes consumed by the stress run: $((after_sent - before_sent)) (${ETCD_INFRA_CLIENT:-official} client)"