#!/usr/bin/env bash
# Runs the snap.db directory-fsync durability E2E tests on AWS EC2: builds
# gofail-enabled linux/amd64 binaries from the fix and control commits,
# uploads them to S3, brings up one cluster per image with
# "etcd-infra aws up --binary-url", runs the env-gated AWS E2E tests, and
# tears the clusters down.
#
# Required environment:
#   AWS_REGION (or AWS default region)
#   ETCD_INFRA_AWS_VPC               existing VPC ID
#   ETCD_INFRA_AWS_AMI               Linux AMI with systemd, curl, tar,
#                                    sha256sum, and a running SSM agent
#   ETCD_INFRA_AWS_INSTANCE_PROFILE  IAM instance profile with SSM permissions
#   ETCD_INFRA_AWS_S3_BUCKET         bucket for the binary uploads
#
# Optional:
#   ETCD_INFRA_AWS_SUBNET            existing subnet ID
#   ETCD_INFRA_AWS_SECURITY_GROUPS   comma-separated security group IDs; must
#                                    allow member-to-member TCP 2379 and 2380,
#                                    and TCP 2379 from this host (the tests
#                                    talk to the members directly)
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cluster_base="etcd-infra-aws-snapdb"

for var in ETCD_INFRA_AWS_VPC ETCD_INFRA_AWS_AMI ETCD_INFRA_AWS_INSTANCE_PROFILE ETCD_INFRA_AWS_S3_BUCKET; do
    if [[ -z "${!var:-}" ]]; then
        echo "${var} is required" >&2
        exit 2
    fi
done
command -v aws >/dev/null 2>&1 || { echo "the AWS CLI is required" >&2; exit 2; }

cleanup() {
    for flavor in control fix; do
        "${project_root}/bin/etcd-infra" aws down --name "${cluster_base}-${flavor}" >/dev/null 2>&1 || true
    done
}
trap cleanup EXIT

# Build gofail-enabled linux/amd64 binaries (this also tags the matching
# container images, which this script does not use).
ETCD_INFRA_SNAPDB_ARCH=amd64 "${project_root}/hack/snapdb/build.sh"
"${project_root}/hack/build.sh"

# upload prints "<sha256> <presigned-url>" for one flavor's binary.
upload() {
    local flavor="$1"
    local binary="${project_root}/.release-work/snapdb/image-${flavor}/etcd"
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

    local up_args=(
        --name "${name}" --members 3 --arch amd64
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
    "${project_root}/bin/etcd-infra" aws up "${up_args[@]}"

    ETCD_INFRA_AWS_E2E_CLUSTER="${name}" \
        ETCD_INFRA_AWS_E2E_FLAVOR="${flavor}" \
        GOCACHE="${GOCACHE:-${project_root}/.release-work/go-build}" \
        go test -run "^(${tests})$" -count=1 -timeout=60m -v "${project_root}/cmd/etcd-infra"

    "${project_root}/bin/etcd-infra" aws down --name "${name}"
}

# The snapDBRenameBeforeDirSync and snapDBDirSyncError failpoints exist only
# in the fixed build. The control run covers the blast radius and the
# no-journal power-loss repro, where the bug must fire live; the fixed run
# adds the crash-window, loud-failure, and both power-loss tests, where the
# member must survive every time.
run_flavor control 'TestSnapDBDirentLostAWSE2E|TestSnapDBHardPowerLossNoJournalControlAWSE2E'
run_flavor fix 'TestSnapDBReceiveCrashWindowAWSE2E|TestSnapDBDirSyncErrorAWSE2E|TestSnapDBDirentLostAWSE2E|TestSnapDBHardPowerLossAWSE2E|TestSnapDBHardPowerLossNoJournalFixAWSE2E'
