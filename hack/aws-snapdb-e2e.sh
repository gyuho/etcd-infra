#!/usr/bin/env bash
# AWS E2E for the snap.db directory-fsync fix. Creates two 3-member clusters
# (control image = pre-fix parent commit, fix image = test branch head), runs
# the durability suite on each via "etcd-infra aws drive" — the compiled test
# binary ships through S3 and runs on each cluster's stress client inside
# the VPC. No tunnels, no public etcd ingress. Both flavors always run; the
# script exits non-zero when either fails.
#
# Required environment: AWS_REGION, ETCD_INFRA_AWS_VPC, ETCD_INFRA_AWS_AMI,
# ETCD_INFRA_AWS_INSTANCE_PROFILE (optional: ETCD_INFRA_AWS_SUBNET,
# ETCD_INFRA_AWS_SECURITY_GROUPS, ETCD_INFRA_AWS_S3_BUCKET).
# Credentials: the least-privilege user from hack/aws-e2e.iam-policy.json.
# The script sets ETCD_INFRA_AWS_E2E_CLUSTER and ETCD_INFRA_AWS_E2E_FLAVOR on
# the stress clients to gate the go tests onto each cluster it brings up.
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cluster_base="etcd-infra-aws-snapdb"

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
        for flavor in control fix; do
            "${tmpdir}/etcd-infra" aws down --name "${cluster_base}-${flavor}" || echo "WARN: aws down failed for ${cluster_base}-${flavor}; cluster may leak — check ~/.etcd-infra/aws/ and EC2" >&2
        done
    fi
    rm -rf "${tmpdir}"
}
trap 'rc=$?; cleanup "$rc"; exit "$rc"' EXIT

"${project_root}/hack/build.sh"
cp "${project_root}/bin/etcd-infra" "${tmpdir}/etcd-infra"
bucket="$(ensure_aws_bucket)"

# Build gofail-enabled linux/amd64 server binaries for both flavors.
ETCD_INFRA_SNAPDB_ARCH=amd64 "${project_root}/hack/snapdb/build.sh"
build_linux_test_binary "${tmpdir}/etcd-infra-e2e.test"

# upload prints "<sha256> <presigned-url>" for one flavor's server binary.
upload() {
    local flavor="$1"
    local binary="${project_root}/.release-work/snapdb/image-${flavor}-amd64/etcd"
    local sha key url
    sha="$(shasum -a 256 "${binary}" | awk '{print $1}')"
    key="etcd-infra/snapdb/${sha}/etcd-${flavor}"
    aws s3 cp "${binary}" "s3://${bucket}/${key}" >/dev/null
    url="$(aws s3 presign "s3://${bucket}/${key}" --expires-in 21600)"
    echo "${sha} ${url}"
}

run_flavor() {
    local flavor="$1" tests="$2"
    local name="${cluster_base}-${flavor}"
    local out; out="$(upload "${flavor}")"
    local sha="${out%% *}" url="${out#* }"

    local up_args=(
        --name "${name}" --members 3 --stress-clients 1
        --vpc "${ETCD_INFRA_AWS_VPC}"
        --ami "${ETCD_INFRA_AWS_AMI}"
        --instance-profile "${ETCD_INFRA_AWS_INSTANCE_PROFILE}"
        --binary-url "${url}"
        --binary-sha256 "${sha}"
        --extra-args "--snapshot-count=10 --snapshot-catchup-entries=10"
        --env "GOFAIL_HTTP=127.0.0.1:2234"
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
    # Do not run the tests against a cluster that never came up: with the
    # outer "|| flavor_exit" disabling set -e for this function, an up failure
    # must return before the tests, or every test fails on a broken cluster.
    if ! "${tmpdir}/etcd-infra" aws up "${up_args[@]}"; then
        echo "aws up failed for ${name}; skipping its tests" >&2
        return 1
    fi

    local results_root="/tmp/etcd-infra-snapdb-${name}-$(date +%s)"
    local test_exit=0
    "${tmpdir}/etcd-infra" aws drive --name "${name}" \
        --binary "${tmpdir}/etcd-infra-e2e.test" --bucket "${bucket}" \
        --suite test \
        --args "-test.run '${tests}' -test.v -test.timeout 120m" \
        --env "ETCD_INFRA_AWS_E2E_CLUSTER=${name},ETCD_INFRA_AWS_E2E_FLAVOR=${flavor}" \
        --results-dir "${results_root}" \
        --timeout 3h || test_exit=$?

    echo "results (${flavor}): ${results_root}"
    if ! "${tmpdir}/etcd-infra" aws down --name "${name}"; then
        echo "WARN: aws down failed for ${name}; cluster may leak — check ~/.etcd-infra/aws/ and EC2" >&2
        test_exit=1
    fi
    return "${test_exit}"
}

control_exit=0 fix_exit=0
run_flavor control "TestSnapDBDirentLostAWSE2E|TestSnapDBHardPowerLossNoJournalControlAWSE2E" || control_exit=$?
run_flavor fix "TestSnapDBReceiveCrashWindowAWSE2E|TestSnapDBDirSyncErrorAWSE2E|TestSnapDBDirentLostAWSE2E|TestSnapDBHardPowerLossAWSE2E|TestSnapDBHardPowerLossNoJournalFixAWSE2E" || fix_exit=$?

if [[ "${control_exit}" -ne 0 || "${fix_exit}" -ne 0 ]]; then
    echo "snap.db AWS E2E failed: control exit ${control_exit}, fix exit ${fix_exit}" >&2
    exit 1
fi
echo "snap.db AWS E2E passed on both flavors"
