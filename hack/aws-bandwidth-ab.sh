#!/usr/bin/env bash
# Measures the peer-bandwidth effect of leader-aware client routing on AWS:
# one cluster, one controlled mutation workload, two client builds. For each
# build the script snapshots etcd_network_peer_sent_bytes_total on every
# member, runs the same stress scenario, snapshots again, and prints the A/B
# comparison. etcd_network_peer_sent_bytes_total counts serialized Raft
# messages, so the follower-to-leader proposal forward that leader-aware
# routing avoids shows up directly.
#
# Required environment: AWS_REGION, ETCD_INFRA_AWS_VPC, ETCD_INFRA_AWS_AMI,
# ETCD_INFRA_AWS_INSTANCE_PROFILE (plus ETCD_INFRA_AWS_SUBNET /
# ETCD_INFRA_AWS_SECURITY_GROUPS optionally) — same as
# hack/aws-conformance-stress-e2e.sh. Credentials: the least-privilege user
# from hack/aws-e2e.iam-policy.json.
#
# Optional: ETCD_INFRA_AWS_STRESS_DURATION (default 120),
# ETCD_INFRA_AWS_STRESS_WORKERS (10), ETCD_INFRA_AWS_STRESS_RPS (100).
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cluster="etcd-infra-aws-bandwidth-ab"

for var in ETCD_INFRA_AWS_VPC ETCD_INFRA_AWS_AMI ETCD_INFRA_AWS_INSTANCE_PROFILE; do
    if [[ -z "${!var:-}" ]]; then
        echo "${var} is required" >&2
        exit 2
    fi
done
command -v aws >/dev/null 2>&1 || { echo "the AWS CLI is required" >&2; exit 2; }
command -v session-manager-plugin >/dev/null 2>&1 || { echo "session-manager-plugin is required" >&2; exit 2; }

tmpdir="$(mktemp -d)"
tunnel_pid=""
cleanup() {
    if [[ -n "${tunnel_pid}" ]]; then
        kill "${tunnel_pid}" >/dev/null 2>&1 || true
    fi
    "${project_root}/bin/etcd-infra" aws down --name "${cluster}" >/dev/null 2>&1 \
        || echo "WARN: aws down failed for ${cluster}; cluster may leak — check ~/.etcd-infra/aws/ and EC2" >&2
    rm -rf "${tmpdir}"
}
trap cleanup EXIT

# Build both binaries up front so nothing rebuilds between the A and B runs.
ETCD_INFRA_CLIENT=official "${project_root}/hack/build.sh"
cp "${project_root}/bin/etcd-infra" "${tmpdir}/etcd-infra-official"
ETCD_INFRA_CLIENT=custom "${project_root}/hack/build.sh"
cp "${project_root}/bin/etcd-infra" "${tmpdir}/etcd-infra-custom"

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

"${project_root}/bin/etcd-infra" aws tunnel --name "${cluster}" \
    > "${tmpdir}/endpoints" 2> "${tmpdir}/tunnel.log" &
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

total_sent() { awk '/^TOTAL/ {print $2}' "$1"; }

run_leg() {
    local label="$1" binary="$2"
    "${binary}" metrics --endpoints "${endpoints}" > "${tmpdir}/${label}-before.txt"
    ETCD_INFRA_SLOW_PATH_MULTIPLIER="${ETCD_INFRA_SLOW_PATH_MULTIPLIER:-2}" \
    "${binary}" stress --endpoints "${endpoints}" \
        --scenario CONCURRENT_PUTS \
        --duration "${ETCD_INFRA_AWS_STRESS_DURATION:-120}" \
        --workers "${ETCD_INFRA_AWS_STRESS_WORKERS:-10}" \
        --rps "${ETCD_INFRA_AWS_STRESS_RPS:-100}"
    "${binary}" metrics --endpoints "${endpoints}" > "${tmpdir}/${label}-after.txt"
    echo "$(( $(total_sent "${tmpdir}/${label}-after.txt") - $(total_sent "${tmpdir}/${label}-before.txt") ))"
}

official_delta="$(run_leg official "${tmpdir}/etcd-infra-official")"
leader_aware_delta="$(run_leg leader-aware "${tmpdir}/etcd-infra-custom")"

echo
echo "=== peer-sent bytes A/B (same cluster, same CONCURRENT_PUTS workload) ==="
echo "official client:      ${official_delta}"
echo "leader-aware client:  ${leader_aware_delta}"
if [[ "${official_delta}" -gt 0 ]]; then
    reduction=$(awk -v a="${official_delta}" -v b="${leader_aware_delta}" 'BEGIN { printf "%.1f", (a - b) * 100 / a }')
    echo "reduction:            ${reduction}%"
fi