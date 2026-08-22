#!/usr/bin/env bash
# Runs the AWS machine-replacement E2E tests (leader handoff and follower
# replacement): brings up one release-image cluster with "etcd-infra aws up
# --replaceable --bastion", runs the env-gated tests, and tears the cluster
# down. Replacement preserves each member's identity (name, private IP) and
# data (a dedicated EBS volume with DeleteOnTermination=false); client
# traffic reaches the members over SSM port-forwarding through the bastion.
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
#
# The test host needs the AWS CLI and session-manager-plugin in PATH.
# Credentials: use the least-privilege user from hack/aws-e2e.iam-policy.json.
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
command -v session-manager-plugin >/dev/null 2>&1 || { echo "session-manager-plugin is required: the tests reach the members over SSM port-forwarding through the bastion" >&2; exit 2; }

tmpdir="$(mktemp -d)"
cleanup() {
    if [[ -x "${tmpdir}/etcd-infra" ]]; then
        "${tmpdir}/etcd-infra" aws down --name "${cluster}" || echo "WARN: aws down failed for ${cluster}; cluster may leak — check ~/.etcd-infra/aws/ and EC2" >&2
    fi
    rm -rf "${tmpdir}"
}
trap cleanup EXIT

"${project_root}/hack/build.sh"
# A private copy: a concurrently running suite rebuilds bin/etcd-infra.
cp "${tmpdir}/etcd-infra" "${tmpdir}/etcd-infra"
cleanup

up_args=(
    --name "${cluster}" --members 3 --bastion --replaceable
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

# Each test replaces one member end to end (terminate, relaunch, reattach,
# re-bootstrap, rejoin), so the go test timeout must cover two full
# replacements plus election and convergence waits.
ETCD_INFRA_AWS_E2E_CLUSTER="${cluster}"     GOCACHE="${GOCACHE:-${project_root}/.release-work/go-build}"     go test -run "^(TestAWSReplaceLeaderHandoffAWSE2E|TestAWSReplaceFollowerAWSE2E)$"     -count=1 -timeout=60m -v "${project_root}/cmd/etcd-infra"