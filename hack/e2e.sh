#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cluster="etcd-infra-e2e"
first_port=32379
endpoints="http://127.0.0.1:32379,http://127.0.0.1:32380,http://127.0.0.1:32381"

tmpdir="$(mktemp -d)"
cleanup() {
    if [[ -x "${tmpdir}/etcd-infra" ]]; then
        "${tmpdir}/etcd-infra" local down --name "${cluster}" >/dev/null 2>&1 || true
    fi
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

"${project_root}/hack/build.sh"
# A private copy: a concurrently running suite rebuilds bin/etcd-infra.
cp "${tmpdir}/etcd-infra" "${tmpdir}/etcd-infra"
cleanup
"${tmpdir}/etcd-infra" local up --name "${cluster}" --members 3 --port "${first_port}"
"${tmpdir}/etcd-infra" conformance --endpoints "${endpoints}" --scenario CLUSTER_MEMBER_LIST
if [[ "${ETCD_INFRA_CLIENT:-official}" == "custom" ]]; then
    ETCD_INFRA_E2E_CLUSTER="${cluster}" ETCD_INFRA_E2E_PORT="${first_port}" \
        GOCACHE="${GOCACHE:-${project_root}/.release-work/go-build}" \
        go test -modfile="${project_root}/go.custom.mod" -tags=etcd_infra_custom_client \
        -run '^TestLeaderAware(Performance|Reliability|Replacement)E2E$' -count=1 -timeout=20m -v "${project_root}/cmd/etcd-infra"
else
    "${tmpdir}/etcd-infra" local replace --name "${cluster}" --members 3 --port "${first_port}" --member leader
fi
"${tmpdir}/etcd-infra" conformance --endpoints "${endpoints}" --scenario CLUSTER_MEMBER_LIST
"${tmpdir}/etcd-infra" stress --endpoints "${endpoints}" --scenario CONCURRENT_PUTS --duration 3 --workers 2 --rps 20
