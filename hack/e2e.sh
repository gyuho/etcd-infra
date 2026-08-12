#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cluster="etcd-infra-e2e"
first_port=32379
endpoints="http://127.0.0.1:32379,http://127.0.0.1:32380,http://127.0.0.1:32381"

cleanup() {
    "${project_root}/bin/etcd-infra" local down --name "${cluster}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

"${project_root}/hack/build.sh"
cleanup
"${project_root}/bin/etcd-infra" local up --name "${cluster}" --members 3 --port "${first_port}"
"${project_root}/bin/etcd-infra" conformance --endpoints "${endpoints}" --scenario CLUSTER_MEMBER_LIST
if [[ "${ETCD_INFRA_CLIENT:-official}" == "custom" ]]; then
    ETCD_INFRA_E2E_CLUSTER="${cluster}" ETCD_INFRA_E2E_PORT="${first_port}" \
        GOCACHE="${GOCACHE:-${project_root}/.release-work/go-build}" \
        go test -modfile="${project_root}/go.custom.mod" -tags=etcd_infra_custom_client \
        -run '^TestLeaderAware(Performance|Reliability|Replacement)E2E$' -count=1 -timeout=20m -v "${project_root}/cmd/etcd-infra"
else
    "${project_root}/bin/etcd-infra" local replace --name "${cluster}" --members 3 --port "${first_port}" --member leader
fi
"${project_root}/bin/etcd-infra" conformance --endpoints "${endpoints}" --scenario CLUSTER_MEMBER_LIST
"${project_root}/bin/etcd-infra" stress --endpoints "${endpoints}" --scenario CONCURRENT_PUTS --duration 3 --workers 2 --rps 20
