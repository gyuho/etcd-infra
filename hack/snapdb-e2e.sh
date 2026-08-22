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

tmpdir="$(mktemp -d)"
teardown_cluster() {
    if [[ -x "${tmpdir}/etcd-infra" ]]; then
        "${tmpdir}/etcd-infra" local down --name "${cluster}" >/dev/null 2>&1 || true
    fi
}
cleanup() {
    teardown_cluster
    rm -rf "${tmpdir}"
}
trap cleanup EXIT

# Preflight: Podman on macOS can wedge its host port forwarder (gvproxy)
# after days of VM uptime. A wedged gvproxy leaks listeners for deleted
# containers, spams "accept tcp ...: use of closed network connection" in its
# log, and then hangs or rejects new port mappings ("proxy already running"),
# which fails container creation deep inside a test run. Publish one probe
# port up front and fail fast with guidance instead.
if [[ "${ETCD_INFRA_CONTAINER_RUNTIME:-podman}" == "podman" ]]; then
    probe_image="gcr.io/etcd-development/etcd:v3.7.1"
    podman pull --quiet "${probe_image}" >/dev/null 2>&1 || true
    podman run --rm --publish 127.0.0.1:32399:2379 "${probe_image}" /usr/local/bin/etcd --version >/dev/null 2>&1 &
    probe_pid=$!
    probe_waited=0
    while kill -0 "${probe_pid}" 2>/dev/null; do
        sleep 1
        probe_waited=$((probe_waited + 1))
        if (( probe_waited >= 30 )); then
            kill -9 "${probe_pid}" 2>/dev/null || true
            echo "ERROR: publishing a container port hung; podman's gvproxy port forwarder is likely wedged." >&2
            echo "Restart the Podman machine: podman machine stop && podman machine start" >&2
            exit 1
        fi
    done
    if ! wait "${probe_pid}"; then
        echo "ERROR: probe container with a published port failed; check podman (gvproxy port forwarder)." >&2
        echo "If the machine has been up for days, restart it: podman machine stop && podman machine start" >&2
        exit 1
    fi
fi

"${project_root}/hack/snapdb/build.sh"
"${project_root}/hack/build.sh"
# A private copy: a concurrently running suite rebuilds bin/etcd-infra.
cp "${project_root}/bin/etcd-infra" "${tmpdir}/etcd-infra"

run_case() {
    local flavor="$1" test="$2" failpoints="$3"
    local image="localhost/etcd-infra-etcd:snapdb-${flavor}-$(uname -m | sed 's/x86_64/amd64/;s/amd64/amd64/;s/aarch64/arm64/')"
    # Arm the failpoint from process boot via GOFAIL_FAILPOINTS: setting it
    # over HTTP after a member restart would race the leader's snapshot
    # stream, which can arrive within milliseconds of boot.
    local env="GOFAIL_HTTP=0.0.0.0:2234"
    if [[ -n "${failpoints}" ]]; then
        env="${env},GOFAIL_FAILPOINTS=${failpoints}"
    fi
    teardown_cluster
    "${tmpdir}/etcd-infra" local up --name "${cluster}" --members 3 --port "${first_port}" \
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
