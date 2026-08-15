#!/usr/bin/env bash
# Runs the snap.db directory-fsync durability E2E tests against local
# container clusters built from the fix and control images.
#
#   ./hack/snapdb-e2e.sh
#
# Set ETCD_INFRA_CONTAINER_RUNTIME=docker to use Docker instead of Podman.
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cluster="etcd-infra-snapdb"
first_port=33379
gofail_port=33479

cleanup() {
    "${project_root}/bin/etcd-infra" local down --name "${cluster}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

"${project_root}/hack/snapdb/build.sh"
"${project_root}/hack/build.sh"

run_case() {
    local flavor="$1" test="$2" failpoints="$3"
    local image="localhost/etcd-infra-etcd:snapdb-${flavor}"
    # Arm the failpoint from process boot via GOFAIL_FAILPOINTS: setting it
    # over HTTP after a member restart would race the leader's snapshot
    # stream, which can arrive within milliseconds of boot.
    local env="GOFAIL_HTTP=0.0.0.0:2234"
    if [[ -n "${failpoints}" ]]; then
        env="${env},GOFAIL_FAILPOINTS=${failpoints}"
    fi
    cleanup
    "${project_root}/bin/etcd-infra" local up --name "${cluster}" --members 3 --port "${first_port}" \
        --image "${image}" \
        --extra-args "--snapshot-count=10 --snapshot-catchup-entries=10 --log-level=info" \
        --env "${env}" \
        --aux-port "2234:${gofail_port}"
    ETCD_INFRA_E2E_CLUSTER="${cluster}" \
        ETCD_INFRA_E2E_PORT="${first_port}" \
        ETCD_INFRA_E2E_GOFAIL_PORT="${gofail_port}" \
        ETCD_INFRA_E2E_IMAGE="${image}" \
        GOCACHE="${GOCACHE:-${project_root}/.release-work/go-build}" \
        go test -run "^${test}$" -count=1 -timeout=15m -v "${project_root}/cmd/etcd-infra"
}

# The snapDBRenameBeforeDirSync and snapDBDirSyncError failpoints exist only in
# the fixed build; the control run documents the blast radius the fix prevents.
run_case control TestSnapDBDirentLostLocalE2E 'applyBeforeOpenSnapshot=sleep("60s")'
run_case fix TestSnapDBReceiveCrashWindowLocalE2E 'snapDBRenameBeforeDirSync=sleep("30s")'
run_case fix TestSnapDBDirSyncErrorLocalE2E 'snapDBDirSyncError=return("injected snap dir fsync failure")'
run_case fix TestSnapDBDirentLostLocalE2E 'applyBeforeOpenSnapshot=sleep("60s")'
