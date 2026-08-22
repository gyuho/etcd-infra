#!/usr/bin/env bash
# AWS E2E for member replacement: replaces the cluster leader and a follower
# end to end. The tests execute on the cluster's stress client instance
# in-VPC ("etcd-infra aws drive"): the compiled test binary ships through S3
# and its output comes back through S3. No tunnels, no public etcd ingress.
#
# Required environment: AWS_REGION, ETCD_INFRA_AWS_VPC, ETCD_INFRA_AWS_AMI,
# ETCD_INFRA_AWS_INSTANCE_PROFILE (optional: ETCD_INFRA_AWS_SUBNET,
# ETCD_INFRA_AWS_SECURITY_GROUPS, ETCD_INFRA_AWS_VERSION,
# ETCD_INFRA_AWS_S3_BUCKET).
# Credentials: the least-privilege user from hack/aws-e2e.iam-policy.json.
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cluster="etcd-infra-aws-replace"

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
trap 'rc=$?; cleanup "$rc"; exit "$rc"' EXIT

"${project_root}/hack/build.sh"
cp "${project_root}/bin/etcd-infra" "${tmpdir}/etcd-infra"
bucket="$(ensure_aws_bucket)"
build_linux_test_binary "${tmpdir}/etcd-infra-e2e.test"

"${tmpdir}/etcd-infra" aws down --name "${cluster}" >/dev/null 2>&1 || true

up_args=(
    --name "${cluster}" --members 3 --stress-clients 1 --replaceable
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

results_root="/tmp/etcd-infra-replace-${cluster}-$(date +%s)"
test_exit=0
"${tmpdir}/etcd-infra" aws drive --name "${cluster}" \
    --binary "${tmpdir}/etcd-infra-e2e.test" --bucket "${bucket}" \
    --suite test \
    --args "-test.run 'TestAWSReplaceLeaderHandoffAWSE2E|TestAWSReplaceFollowerAWSE2E' -test.v -test.timeout 30m" \
    --env "ETCD_INFRA_AWS_E2E_CLUSTER=${cluster}" \
    --results-dir "${results_root}" \
    --timeout 1h || test_exit=$?

# The replacement rewrote the cluster state on the stress client; pull the
# updated state back so the local "aws down" sees the new member.
for f in "${results_root}/"*/state.json; do
    [[ -f "${f}" ]] || continue
    cp "${f}" "${HOME}/.etcd-infra/aws/${cluster}.json"
done

echo "results: ${results_root}"
exit "${test_exit}"
