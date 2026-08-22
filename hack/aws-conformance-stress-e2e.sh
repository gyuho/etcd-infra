#!/usr/bin/env bash
# Runs the conformance and stress suites against a 3-member etcd cluster on
# AWS. The suites execute on the cluster's stress client instance, inside
# the VPC: the binaries ship through S3 and the results come back through S3
# ("etcd-infra aws drive"). No tunnels, no public etcd ingress.
#
# Required environment:
#   AWS_REGION                       AWS region
#   ETCD_INFRA_AWS_VPC               VPC ID for the cluster
#   ETCD_INFRA_AWS_AMI               Linux AMI with systemd, curl, tar, sha256sum, SSM agent
#   ETCD_INFRA_AWS_INSTANCE_PROFILE  IAM instance profile with SSM permissions
# Optional: ETCD_INFRA_AWS_SUBNET, ETCD_INFRA_AWS_SECURITY_GROUPS,
#   ETCD_INFRA_AWS_VERSION (etcd release, default: latest),
#   ETCD_INFRA_AWS_STRESS_DURATION   seconds per stress scenario per leg (default: 60)
#   ETCD_INFRA_AWS_STRESS_WORKERS    stress workers (default: 10)
#   ETCD_INFRA_AWS_STRESS_RPS        requests per second (default: 100)
#   ETCD_INFRA_AWS_STRESS_SCENARIO   comma-separated stress scenario IDs (default: all)
#   ETCD_INFRA_SLOW_PATH_MULTIPLIER  latency-budget multiplier (default: 1; in-VPC paths need none)
#   ETCD_INFRA_AWS_S3_BUCKET         override the derived monthly bucket
#
# Credentials: run this with the least-privilege user from
# hack/aws-e2e.iam-policy.json (tagged-instance EC2 lifecycle in one region,
# SSM on tagged instances only, the binary bucket prefix, PassRole to the SSM
# instance-profile role) so the tests cannot touch EKS or any other AWS
# resources.
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

. "${project_root}/hack/aws-e2e-lib.sh"

tmpdir="$(mktemp -d)"
cleanup() {
    if [[ -x "${tmpdir}/etcd-infra" ]]; then
        "${tmpdir}/etcd-infra" aws down --name "${cluster}" || echo "WARN: aws down failed for ${cluster}; cluster may leak — check ~/.etcd-infra/aws/ and EC2" >&2
    fi
    rm -rf "${tmpdir}"
}
trap 'rc=$?; cleanup; exit "$rc"' EXIT

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

conformance_args=(--scenario "${ETCD_INFRA_AWS_CONFORMANCE_SCENARIO:-}")
if [[ -z "${ETCD_INFRA_AWS_CONFORMANCE_SCENARIO:-}" ]]; then
    conformance_args=()
fi
results_root="/tmp/etcd-infra-results-${cluster}-$(date +%s)"
"${tmpdir}/etcd-infra" aws drive --name "${cluster}" \
    --binary "${tmpdir}/etcd-infra-official" --bucket "${bucket}" \
    --suite conformance --args "${conformance_args[*]:-}" \
    --results-dir "${results_root}/conformance" \
    --timeout 2h

stress_args=(--duration "${ETCD_INFRA_AWS_STRESS_DURATION:-60}" --workers "${ETCD_INFRA_AWS_STRESS_WORKERS:-10}" --rps "${ETCD_INFRA_AWS_STRESS_RPS:-100}")
if [[ -n "${ETCD_INFRA_AWS_STRESS_SCENARIO:-}" ]]; then
    stress_args+=(--scenario "${ETCD_INFRA_AWS_STRESS_SCENARIO}")
fi

run_stress_leg() {
    local label="$1" binary="$2"
    "${tmpdir}/etcd-infra" aws drive --name "${cluster}" \
        --binary "${binary}" --bucket "${bucket}" \
        --suite stress --args "${stress_args[*]}" \
        --env "ETCD_INFRA_SLOW_PATH_MULTIPLIER=${ETCD_INFRA_SLOW_PATH_MULTIPLIER:-1}" \
        --results-dir "${results_root}/stress-${label}" \
        --timeout 2h
}

run_stress_leg official "${tmpdir}/etcd-infra-official"
run_stress_leg custom "${tmpdir}/etcd-infra-custom"

echo
echo "=== peer-sent bytes per stress leg (summed across members and clients) ==="
for label in official custom; do
    before=$(awk '{s+=$1} END{print s+0}' "${results_root}/stress-${label}/"*/metrics-before.txt)
    after=$(awk '{s+=$1} END{print s+0}' "${results_root}/stress-${label}/"*/metrics-after.txt)
    echo "${label}: $((after - before)) bytes"
done
echo "results: ${results_root}"
