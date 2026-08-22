#!/usr/bin/env bash
# Controlled A/B: identical short CONCURRENT_PUTS runs against the same
# cluster with the official and the leader-aware client, measuring the
# peer-sent bytes each consumes. Suites run on the cluster's stress client
# instances in-VPC via "etcd-infra aws drive"; metrics come from the
# members' /metrics endpoints over the VPC.
#
# Required environment: same as hack/aws-conformance-stress-e2e.sh.
# Credentials: the least-privilege user from hack/aws-e2e.iam-policy.json.
# Optional: ETCD_INFRA_AWS_STRESS_DURATION (default 120),
# ETCD_INFRA_AWS_STRESS_WORKERS (10), ETCD_INFRA_AWS_STRESS_RPS (100),
# ETCD_INFRA_AWS_VERSION, ETCD_INFRA_AWS_S3_BUCKET.
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

. "${project_root}/hack/aws-e2e-lib.sh"

tmpdir="$(mktemp -d)"
cleanup() {
    if [[ -x "${tmpdir}/etcd-infra" ]]; then
        "${tmpdir}/etcd-infra" aws down --name "${cluster}" >/dev/null 2>&1 \
            || echo "WARN: aws down failed for ${cluster}; cluster may leak — check ~/.etcd-infra/aws/ and EC2" >&2
    fi
    rm -rf "${tmpdir}"
}
trap 'rc=$?; cleanup "$rc"; exit "$rc"' EXIT

"${project_root}/hack/build.sh"
cp "${project_root}/bin/etcd-infra" "${tmpdir}/etcd-infra"
bucket="$(ensure_aws_bucket)"
build_linux_driver_binaries "${tmpdir}"

"${tmpdir}/etcd-infra" aws down --name "${cluster}" >/dev/null 2>&1 || true

up_args=(
    --name "${cluster}" --members 3 --stress-clients 1
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
"${tmpdir}/etcd-infra" aws up "${up_args[@]}"

results_root="/tmp/etcd-infra-ab-${cluster}-$(date +%s)"
args="--scenario CONCURRENT_PUTS --duration ${ETCD_INFRA_AWS_STRESS_DURATION:-120} --workers ${ETCD_INFRA_AWS_STRESS_WORKERS:-10} --rps ${ETCD_INFRA_AWS_STRESS_RPS:-100}"

"${tmpdir}/etcd-infra" aws drive --name "${cluster}" --binary "${tmpdir}/etcd-infra-official" --bucket "${bucket}" \
    --suite stress --args "${args}" --results-dir "${results_root}/official" --timeout 1h
"${tmpdir}/etcd-infra" aws drive --name "${cluster}" --binary "${tmpdir}/etcd-infra-custom" --bucket "${bucket}" \
    --suite stress --args "${args}" --results-dir "${results_root}/custom" --timeout 1h

delta() {
    local before after
    before=$(awk '{s+=$1} END{print s+0}' "${results_root}/$1/"*/metrics-before.txt)
    after=$(awk '{s+=$1} END{print s+0}' "${results_root}/$1/"*/metrics-after.txt)
    echo $((after - before))
}
off="$(delta official)"
cust="$(delta custom)"

echo
echo "=== peer-sent bytes A/B (same cluster, same CONCURRENT_PUTS workload) ==="
echo "official client:      ${off}"
echo "leader-aware client:  ${cust}"
python3 -c "o, c = ${off}, ${cust}; print(f'reduction:            {(o-c)*100/o:.1f}%') if o else None"
echo "results: ${results_root}"
