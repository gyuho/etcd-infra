#!/usr/bin/env bash
# Shared helpers for the AWS end-to-end suites. All suites execute on the
# cluster's stress client instances (in-VPC) via "etcd-infra aws drive":
# binaries ship through S3, results come back through S3. No tunnels, no
# public etcd ingress.
#
# Required environment for the AWS CLI: credentials of the least-privilege
# user from hack/aws-e2e.iam-policy.json.

# ensure_aws_bucket derives the monthly binary/result bucket
# (etcd-infra-e2e-<account>-<region>-v0-<YYYYMM>) and creates it with public
# access blocked when missing. Prints the bucket name.
ensure_aws_bucket() {
    local account region bucket
    account="$(aws sts get-caller-identity --query Account --output text)"
    region="${AWS_REGION:-$(aws configure get region 2>/dev/null || true)}"
    if [[ -z "${region}" ]]; then
        echo "AWS_REGION is required" >&2
        return 2
    fi
    bucket="${ETCD_INFRA_AWS_S3_BUCKET:-etcd-infra-e2e-${account}-${region}-v0-$(date -u +%Y%m)}"
    if ! aws s3api head-bucket --bucket "${bucket}" 2>/dev/null; then
        echo "creating bucket ${bucket}" >&2
        if [[ "${region}" == "us-east-1" ]]; then
            aws s3api create-bucket --bucket "${bucket}" >/dev/null
        else
            aws s3api create-bucket --bucket "${bucket}" \
                --create-bucket-configuration "LocationConstraint=${region}" >/dev/null
        fi
        aws s3api put-public-access-block --bucket "${bucket}" \
            --public-access-block-configuration BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true
    fi
    echo "${bucket}"
}

# build_linux_driver_binaries <dir>: cross-build the official and
# leader-aware (custom) etcd-infra binaries for the linux/amd64 stress
# clients into <dir>/etcd-infra-official and <dir>/etcd-infra-custom, and
# leave a fresh host build in bin/.
build_linux_driver_binaries() {
    local dir="$1"
    mkdir -p "${dir}"
    (cd "${project_root}" && ETCD_INFRA_CLIENT=official GOOS=linux GOARCH=amd64 ./hack/build.sh)
    cp "${project_root}/bin/etcd-infra" "${dir}/etcd-infra-official"
    (cd "${project_root}" && ETCD_INFRA_CLIENT=custom GOOS=linux GOARCH=amd64 ./hack/build.sh)
    cp "${project_root}/bin/etcd-infra" "${dir}/etcd-infra-custom"
    (cd "${project_root}" && ETCD_INFRA_CLIENT=official ./hack/build.sh)
}

# build_linux_test_binary <out>: compile the cmd/etcd-infra e2e test binary
# for the linux/amd64 stress clients.
build_linux_test_binary() {
    local out="$1"
    (cd "${project_root}" && GOOS=linux GOARCH=amd64 GOFLAGS="-buildvcs=false" \
        GOCACHE="${GOCACHE:-${project_root}/.release-work/go-build}" \
        go test -c -o "${out}" ./cmd/etcd-infra)
}

