#!/usr/bin/env bash
# Builds gofail-enabled etcd images from the snap.db dir-fsync fix branch and
# its unfixed parent commit, for the snapshot durability E2E tests.
#
#   fix:     gyuho/etcd@d73ad4e (fix/snapdb-dir-fsync) — SaveDBFrom fsyncs the
#            snap directory and exposes the snapDBRenameBeforeDirSync and
#            snapDBDirSyncError failpoints.
#   control: gyuho/etcd@f744d45 (unfixed parent) — SaveDBFrom renames without a
#            directory fsync; used to document the failure mode the fix
#            prevents.
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
work_root="${project_root}/.release-work/snapdb"
runtime="${ETCD_INFRA_CONTAINER_RUNTIME:-podman}"
repo="https://github.com/gyuho/etcd.git"
base_image="gcr.io/etcd-development/etcd:v3.7.1"

fix_sha="d73ad4e85fec490b92615837f966cbd6ea0fc533"
control_sha="f744d457f484e9f748a0700b48ef96dcf792df33"

# The containers run on the container runtime's host (on macOS, the Podman
# machine VM), which matches the host architecture for the supported setups.
case "$(uname -m)" in
    arm64 | aarch64) default_goarch=arm64 ;;
    x86_64 | amd64) default_goarch=amd64 ;;
    *) echo "unsupported host architecture $(uname -m)" >&2; exit 2 ;;
esac
goarch="${ETCD_INFRA_SNAPDB_ARCH:-${default_goarch}}"

build_image() {
    local flavor="$1" sha="$2"
    local image="localhost/etcd-infra-etcd:snapdb-${flavor}"
    if [[ "${ETCD_INFRA_SNAPDB_REBUILD:-0}" != "1" ]] && "${runtime}" image inspect "${image}" >/dev/null 2>&1; then
        echo "[snapdb] ${image} already exists; set ETCD_INFRA_SNAPDB_REBUILD=1 to rebuild"
        return 0
    fi

    local clone="${work_root}/etcd-${flavor}"
    local outdir="${work_root}/image-${flavor}"
    rm -rf "${outdir}"
    mkdir -p "${outdir}"

    if [[ ! -d "${clone}/.git" ]]; then
        rm -rf "${clone}"
        mkdir -p "${clone}"
        git -C "${clone}" init -q
        git -C "${clone}" remote add origin "${repo}"
        git -C "${clone}" fetch -q --depth 1 origin "${sha}"
        git -C "${clone}" checkout -q FETCH_HEAD
    fi

    echo "[snapdb] enabling gofail for ${flavor} (${sha})"
    make -C "${clone}" gofail-enable

    echo "[snapdb] building etcd ${flavor} for linux/${goarch}"
    rm -f "${outdir}/etcd"
    (cd "${clone}/server" && GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH="${goarch}" go build -trimpath -o "${outdir}/etcd" .)

    cp "${project_root}/hack/snapdb/Containerfile" "${outdir}/Containerfile"
    "${runtime}" build --build-arg "BASE_IMAGE=${base_image}" --tag "${image}" "${outdir}"
    echo "[snapdb] built ${image}"
}

build_image control "${control_sha}"
build_image fix "${fix_sha}"
