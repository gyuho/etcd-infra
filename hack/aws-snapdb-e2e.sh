#!/usr/bin/env bash
# Runs the snap.db directory-fsync durability E2E tests on AWS EC2: builds
# gofail-enabled linux/amd64 binaries from the fix and control commits,
# uploads them to S3, brings up one cluster per image with
# "etcd-infra aws up --binary-url --bastion", runs the env-gated AWS E2E
# tests, and tears the clusters down.
#
# Required environment:
#   AWS_REGION (or AWS default region)
#   ETCD_INFRA_AWS_VPC               existing VPC ID
#   ETCD_INFRA_AWS_AMI               Linux AMI with systemd, curl, tar,
#                                    sha256sum, and a running SSM agent
#   ETCD_INFRA_AWS_INSTANCE_PROFILE  IAM instance profile with SSM permissions
#   ETCD_INFRA_AWS_S3_BUCKET         bucket for the binary uploads (optional;
#                                    default derived from account, region, and
#                                    month: etcd-infra-e2e-<account>-<region>-v0-<YYYYMM>,
#                                    created on first use — same name all month)
#
# Optional:
#   ETCD_INFRA_AWS_SUBNET            existing subnet ID
#   ETCD_INFRA_AWS_SECURITY_GROUPS   comma-separated security group IDs; must
#                                    allow member-to-member TCP 2379 and 2380.
#
# Each cluster includes a bastion relay ("aws up --bastion"): the tests reach
# member TCP 2379 over SSM port-forwarding through the bastion, so etcd never
# needs a public ingress rule — mirroring production, where etcd is never
# exposed publicly. The bastion shares the members' security groups, so
# bastion-to-member traffic is covered by the member-to-member rules. The
# test host needs the AWS CLI and session-manager-plugin in PATH. The
# script sets ETCD_INFRA_AWS_E2E_CLUSTER and ETCD_INFRA_AWS_E2E_FLAVOR itself
# to gate the go tests onto each cluster it brings up.
#
# Credentials: run this with the least-privilege user from
# hack/aws-e2e.iam-policy.json (tagged-instance EC2 lifecycle in one region,
# SSM on tagged instances only, the binary bucket prefix, PassRole to the SSM
# instance-profile role) so the tests cannot touch EKS or any other AWS
# resources.
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cluster_base="etcd-infra-aws-snapdb"

for var in ETCD_INFRA_AWS_VPC ETCD_INFRA_AWS_AMI ETCD_INFRA_AWS_INSTANCE_PROFILE; do
    if [[ -z "${!var:-}" ]]; then
        echo "${var} is required" >&2
        exit 2
    fi
done

# Derive the upload bucket when unset: deterministic for the account, region,
# and month, so every run this month lands in the same bucket (the k8x
# scheme: stable within a month, rotated by the name, never per-run).
if [[ -z "${ETCD_INFRA_AWS_S3_BUCKET:-}" ]]; then
    aws_account="$(aws sts get-caller-identity --query Account --output text)"
    bucket_region="${AWS_REGION:-$(aws configure get region || true)}"
    if [[ -z "${bucket_region}" ]]; then
        echo "AWS_REGION (or a configured default region) is required to derive the bucket name" >&2
        exit 2
    fi
    ETCD_INFRA_AWS_S3_BUCKET="etcd-infra-e2e-${aws_account}-${bucket_region}-v0-$(date -u +%Y%m)"
fi
if ! aws s3api head-bucket --bucket "${ETCD_INFRA_AWS_S3_BUCKET}" 2>/dev/null; then
    echo "creating bucket ${ETCD_INFRA_AWS_S3_BUCKET}"
    if [[ "${bucket_region:-${AWS_REGION:-}}" == "us-east-1" ]]; then
        aws s3api create-bucket --bucket "${ETCD_INFRA_AWS_S3_BUCKET}" >/dev/null
    else
        aws s3api create-bucket --bucket "${ETCD_INFRA_AWS_S3_BUCKET}" \
            --create-bucket-configuration "LocationConstraint=${bucket_region:-${AWS_REGION}}" >/dev/null
    fi
    aws s3api put-public-access-block --bucket "${ETCD_INFRA_AWS_S3_BUCKET}" \
        --public-access-block-configuration BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true
fi
command -v aws >/dev/null 2>&1 || { echo "the AWS CLI is required" >&2; exit 2; }
command -v session-manager-plugin >/dev/null 2>&1 || { echo "session-manager-plugin is required: the tests reach the members over SSM port-forwarding through the bastion" >&2; exit 2; }

tmpdir="$(mktemp -d)"
cleanup() {
    if [[ -x "${tmpdir}/etcd-infra" ]]; then
        for flavor in control fix; do
            "${tmpdir}/etcd-infra" aws down --name "${cluster_base}-${flavor}" || echo "WARN: aws down failed for ${cluster_base}-${flavor}; cluster may leak — check ~/.etcd-infra/aws/ and EC2" >&2
        done
    fi
    rm -rf "${tmpdir}"
}
trap cleanup EXIT

# Build gofail-enabled linux/amd64 binaries (this also tags the matching
# container images, which this script does not use).
ETCD_INFRA_SNAPDB_ARCH=amd64 "${project_root}/hack/snapdb/build.sh"
"${project_root}/hack/build.sh"
# A private copy: a concurrently running suite rebuilds bin/etcd-infra.
cp "${tmpdir}/etcd-infra" "${tmpdir}/etcd-infra"

# upload prints "<sha256> <presigned-url>" for one flavor's binary.
upload() {
    local flavor="$1"
    local binary="${project_root}/.release-work/snapdb/image-${flavor}-amd64/etcd"
    local sha key url
    sha="$(shasum -a 256 "${binary}" | awk '{print $1}')"
    key="etcd-infra/snapdb/${sha}/etcd-${flavor}"
    aws s3 cp "${binary}" "s3://${ETCD_INFRA_AWS_S3_BUCKET}/${key}" >/dev/null
    url="$(aws s3 presign "s3://${ETCD_INFRA_AWS_S3_BUCKET}/${key}" --expires-in 86400)"
    echo "${sha} ${url}"
}

run_flavor() {
    local flavor="$1" tests="$2"
    local name="${cluster_base}-${flavor}"
    local sha url
    read -r sha url <<< "$(upload "${flavor}")"
    if [[ -z "${sha}" || -z "${url}" ]]; then
        echo "binary upload or presign failed for ${flavor}" >&2
        exit 1
    fi

    local up_args=(
        --name "${name}" --members 3 --arch amd64 --bastion
        --vpc "${ETCD_INFRA_AWS_VPC}"
        --ami "${ETCD_INFRA_AWS_AMI}"
        --instance-profile "${ETCD_INFRA_AWS_INSTANCE_PROFILE}"
        --binary-url "${url}" --binary-sha256 "${sha}"
        --extra-args "--snapshot-count=10 --snapshot-catchup-entries=10 --log-level=info"
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
    # must return before go test, or every test fails on a broken cluster.
    if ! "${tmpdir}/etcd-infra" aws up "${up_args[@]}"; then
        echo "aws up failed for ${name}; skipping its tests" >&2
        return 1
    fi

    # The per-test context budgets sum to ~95 minutes for the fix flavor;
    # the go test timeout must exceed them or a slow SSM run dies mid-suite.
    # Capture the test result explicitly: the outer "run_flavor ... || exit=$?"
    # disables set -e for this function, and the trailing "aws down" would
    # otherwise mask a go test failure with its own success.
    local test_exit=0
    ETCD_INFRA_AWS_E2E_CLUSTER="${name}" \
        ETCD_INFRA_AWS_E2E_FLAVOR="${flavor}" \
        GOCACHE="${GOCACHE:-${project_root}/.release-work/go-build}" \
        go test -run "^(${tests})$" -count=1 -timeout=120m -v "${project_root}/cmd/etcd-infra" || test_exit=$?

    if ! "${tmpdir}/etcd-infra" aws down --name "${name}"; then
        echo "WARN: aws down failed for ${name}; cluster may leak — check ~/.etcd-infra/aws/ and EC2" >&2
    fi
    return "${test_exit}"
}

# The snapDBRenameBeforeDirSync and snapDBDirSyncError failpoints exist only
# in the fixed build. The control run covers the blast radius and the
# no-journal power-loss repro, where the bug must fire live; the fixed run
# adds the crash-window, loud-failure, and both power-loss tests, where the
# member must survive every time.
# Run both flavors even when one fails: a control failure must not hide the
# fix flavor's evidence. The exit code still reports any failure.
control_exit=0
run_flavor control 'TestSnapDBDirentLostAWSE2E|TestSnapDBHardPowerLossNoJournalControlAWSE2E' || control_exit=$?
fix_exit=0
run_flavor fix 'TestSnapDBReceiveCrashWindowAWSE2E|TestSnapDBDirSyncErrorAWSE2E|TestSnapDBDirentLostAWSE2E|TestSnapDBHardPowerLossAWSE2E|TestSnapDBHardPowerLossNoJournalFixAWSE2E' || fix_exit=$?
if [[ "${control_exit}" -ne 0 || "${fix_exit}" -ne 0 ]]; then
    echo "snap.db AWS E2E failed: control exit ${control_exit}, fix exit ${fix_exit}" >&2
    exit 1
fi