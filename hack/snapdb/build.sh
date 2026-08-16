#!/usr/bin/env bash
# Builds gofail-enabled etcd images from the snap.db dir-fsync fix branch and
# its unfixed parent commit, for the snapshot durability E2E tests.
#
#   fix:     gyuho/etcd branch fix/snapdb-dir-fsync head — SaveDBFrom fsyncs
#            the snap directory and exposes the snapDBRenameBeforeDirSync and
#            snapDBDirSyncError failpoints.
#   control: the fix commit's parent (unfixed) — SaveDBFrom renames without a
#            directory fsync; used to document the failure mode the fix
#            prevents.
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
work_root="${project_root}/.release-work/snapdb"
runtime="${ETCD_INFRA_CONTAINER_RUNTIME:-podman}"
repo="https://github.com/gyuho/etcd.git"
base_image="gcr.io/etcd-development/etcd:v3.7.1"

# The fix branch may be rebased before the upstream PR lands, so resolve the
# commits at build time: fix = branch head, control = its parent (the unfixed
# base). Pin exact commits with ETCD_INFRA_SNAPDB_FIX_SHA /
# ETCD_INFRA_SNAPDB_CONTROL_SHA for reproducibility.
fix_ref="${ETCD_INFRA_SNAPDB_FIX_REF:-fix/snapdb-dir-fsync}"
if [[ -n "${ETCD_INFRA_SNAPDB_FIX_SHA:-}" && -n "${ETCD_INFRA_SNAPDB_CONTROL_SHA:-}" ]]; then
    fix_sha="${ETCD_INFRA_SNAPDB_FIX_SHA}"
    control_sha="${ETCD_INFRA_SNAPDB_CONTROL_SHA}"
else
    # ls-remote resolves branch names that contain slashes (the commits API
    # path does not); the parent lookup then uses the SHA, not the ref.
    fix_sha="$(git ls-remote "${repo}" "refs/heads/${fix_ref}" | awk '{print $1}')"
    if [[ -z "${fix_sha}" ]]; then
        echo "could not resolve ${repo} branch ${fix_ref}" >&2
        exit 1
    fi
    # gh api authenticates; unauthenticated curl hits the rate limit.
    control_sha="$(gh api "repos/gyuho/etcd/commits/${fix_sha}" --jq '.parents[0].sha')"
fi
if [[ -z "${control_sha}" ]]; then
    echo "could not resolve the parent of ${fix_sha}" >&2
    exit 1
fi
echo "[snapdb] fix=${fix_sha} control=${control_sha}"

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
    local stamp="${work_root}/image-${flavor}.sha"
    if [[ "${ETCD_INFRA_SNAPDB_REBUILD:-0}" != "1" ]] \
        && [[ -f "${stamp}" ]] && [[ "$(cat "${stamp}")" == "${sha}" ]] \
        && "${runtime}" image inspect "${image}" >/dev/null 2>&1; then
        echo "[snapdb] ${image} already built from ${sha}; set ETCD_INFRA_SNAPDB_REBUILD=1 to force"
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
    fi
    # Fetch and check out the resolved commit every build: the branch may have
    # been rebased since the cached clone was created, and a previous gofail
    # enable leaves the tree dirty.
    git -C "${clone}" fetch -q --depth 2 origin "${sha}"
    git -C "${clone}" checkout -q -f FETCH_HEAD
    git -C "${clone}" clean -fdq

    echo "[snapdb] enabling gofail for ${flavor} (${sha})"
    make -C "${clone}" gofail-enable

    echo "[snapdb] building etcd ${flavor} for linux/${goarch}"
    rm -f "${outdir}/etcd"
    (cd "${clone}/server" && GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH="${goarch}" go build -trimpath -o "${outdir}/etcd" .)

    cp "${project_root}/hack/snapdb/Containerfile" "${outdir}/Containerfile"
    "${runtime}" build --build-arg "BASE_IMAGE=${base_image}" --tag "${image}" "${outdir}"
    echo "${sha}" > "${stamp}"
    echo "[snapdb] built ${image}"
}

build_image control "${control_sha}"
build_image fix "${fix_sha}"
