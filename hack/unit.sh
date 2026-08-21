#!/usr/bin/env bash
set +o xtrace
set -o nounset
set -o errexit

# do not mask errors in a pipeline
set -o pipefail

if [[ "${TRACE_HACK_SCRIPTS:-0}" == "1" ]]; then
    set -o xtrace
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

version_lt() {
    [[ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | head -n1)" == "$1" && "$1" != "$2" ]]
}

if [[ "$(pwd)" != "$PROJECT_ROOT" ]]; then
    cd "$PROJECT_ROOT"
fi

# Start from the locally installed Go toolchain. If the repo requires a newer
# version, upgrade explicitly below and isolate its cache. Relying on "auto"
# here can mix tool and cache state across toolchains, which breaks -race builds.
export GOTOOLCHAIN="${GOTOOLCHAIN:-local}"
export GOCACHE="${GOCACHE:-$PROJECT_ROOT/.release-work/go-build}"
# The test harness uses package-scoped gcflags so Mockey's all=-N -l probe is not applicable.
export MOCKEY_CHECK_GCFLAGS="${MOCKEY_CHECK_GCFLAGS:-false}"
default_gomodcache="$(go env GOMODCACHE 2>/dev/null || true)"
default_goos="$(go env GOOS 2>/dev/null || true)"
default_goarch="$(go env GOARCH 2>/dev/null || true)"

# ── Dependency checks (fail fast) ──────────────────────────────────────
min_go_version="1.26.1"

if ! command -v go >/dev/null 2>&1; then
    echo "go is required (expected Go ${min_go_version}+). Install Go before running tests." >&2
    exit 127
fi

if ! command -v gofumpt >/dev/null 2>&1; then
    echo "gofumpt is required. Install it with: go install mvdan.cc/gofumpt@latest" >&2
    exit 127
fi

toolchain_version="$(awk '
    /^toolchain / { print $2; exit }
    /^go / { print "go"$2 }
' "$PROJECT_ROOT/go.mod" 2>/dev/null || true)"

current_go_version="$(go env GOVERSION 2>/dev/null | sed 's/^go//')"
if [[ -z "$current_go_version" ]]; then
    echo "Failed to determine Go version. Ensure Go is properly installed." >&2
    exit 1
fi
if [[ -n "$toolchain_version" ]]; then
    toolchain_go_version="${toolchain_version#go}"
    if version_lt "$current_go_version" "$toolchain_go_version"; then
        cached_toolchain_root=""
        if [[ -n "$default_gomodcache" && -n "$default_goos" && -n "$default_goarch" ]]; then
            cached_toolchain_root="${default_gomodcache}/golang.org/toolchain@v0.0.1-${toolchain_version}.${default_goos}-${default_goarch}"
        fi
        if [[ -x "${cached_toolchain_root}/bin/go" ]]; then
            export GOROOT="$cached_toolchain_root"
            export PATH="$GOROOT/bin:$PATH"
            export GOTOOLCHAIN=local
        else
            export GOTOOLCHAIN="$toolchain_version"
        fi
        export GOCACHE="${GOCACHE}/${toolchain_version}"
        current_go_version="$(go env GOVERSION 2>/dev/null | sed 's/^go//')"
        if [[ -z "$current_go_version" ]]; then
            echo "Failed to determine Go version. Ensure Go is properly installed." >&2
            exit 1
        fi
    fi
fi
if [[ "$(printf '%s\n%s\n' "$min_go_version" "$current_go_version" | sort -V | head -n1)" != "$min_go_version" ]]; then
    echo "Go ${min_go_version}+ is required. Found Go ${current_go_version}." >&2
    exit 1
fi
if [[ " ${GOFLAGS:-} " != *" -buildvcs=false "* ]]; then
    export GOFLAGS="${GOFLAGS:+${GOFLAGS} }-buildvcs=false"
fi
client_mode="${ETCD_INFRA_CLIENT:-official}"
case "$client_mode" in
    official) ;;
    custom) export GOFLAGS="${GOFLAGS} -modfile=${PROJECT_ROOT}/go.custom.mod -tags=etcd_infra_custom_client" ;;
    *) echo "ETCD_INFRA_CLIENT must be official or custom" >&2; exit 2 ;;
esac

extra_go_test_flags=()
if [[ -n "${GO_TEST_FLAGS:-}" ]]; then
    read -r -a extra_go_test_flags <<< "${GO_TEST_FLAGS}"
fi

requested_packages=("$@")

log_step() {
    printf '\n[unit] %s\n' "$1"
}

print_command() {
    local part
    printf '[unit] Command:'
    for part in "$@"; do
        printf ' %q' "$part"
    done
    printf '\n'
}

run_unit_tests() {
    local target_packages=()
    local gofumpt_files=()

    if [[ ${#requested_packages[@]} -gt 0 ]]; then
        while IFS= read -r pkg; do
            target_packages+=("$pkg")
        done < <(go list "${requested_packages[@]}")
    else
        while IFS= read -r pkg; do
            target_packages+=("$pkg")
        done < <(go list ./...)
    fi

    if [[ ${#target_packages[@]} -eq 0 ]]; then
        echo "No packages matched the requested test scope." >&2
        exit 1
    fi

    # Format Go files (retry once on transient errors)
    log_step "Formatting Go files"
    while IFS= read -r file; do
        if [[ -n "$file" ]]; then
            gofumpt_files+=("$file")
        fi
    done < <(go list -f '{{range .GoFiles}}{{$.Dir}}/{{.}}{{"\n"}}{{end}}{{range .TestGoFiles}}{{$.Dir}}/{{.}}{{"\n"}}{{end}}{{range .XTestGoFiles}}{{$.Dir}}/{{.}}{{"\n"}}{{end}}' "${target_packages[@]}" | sort -u)

    if [[ ${#gofumpt_files[@]} -gt 0 ]]; then
        if ! gofumpt -extra -modpath git.tbd/etcd-infra -w "${gofumpt_files[@]}"; then
            echo "gofumpt encountered transient error, retrying..." >&2
            sleep 1
            gofumpt -extra -modpath git.tbd/etcd-infra -w "${gofumpt_files[@]}"
        fi
    fi

    log_step "Running go vet"
    go vet -v "${target_packages[@]}"

    # Run all package tests with the race detector enabled.
    #
    # -gcflags are required for github.com/bytedance/mockey to work reliably
    # by disabling inlining (-l) and optimizations (-N).
    #
    # IMPORTANT: Go 1.26+ crashes (SIGSEGV) when "all=-l" is combined with -race,
    # because disabling inlining in the runtime breaks the race detector internals.
    # Scope gcflags to project packages only ("git.tbd/etcd-infra/...=") instead of "all=".
    #
    # -d=checkptr=0 disables checkptr because mockey's unsafe monkey patching
    # triggers false-positive pointer conversion panics under the race detector.
    # References:
    #   https://github.com/bytedance/mockey/blob/main/.github/workflows/tests.yml#L6-L37
    #   https://github.com/golang/go/issues/34964
    # Run packages one-by-one instead of one giant package-list invocation.
    # Several tests in this repo spin up loopback listeners or embedded services,
    # and package-local runs are stable while a single giant go test command is not.
    # Keeping the flags identical but isolating each package avoids cross-package
    # flakiness without weakening coverage or race detection.
    log_step "Running go test on ${#target_packages[@]} packages"
    local pkg
    for pkg in "${target_packages[@]}"; do
        local go_test_cmd=(go test -p 1 -v -cover -race -gcflags="git.tbd/etcd-infra/...=-l -N -d=checkptr=0")
        if [[ ${#extra_go_test_flags[@]} -gt 0 ]]; then
            go_test_cmd+=("${extra_go_test_flags[@]}")
        fi
        go_test_cmd+=("$pkg")

        log_step "Testing ${pkg}"
        print_command "${go_test_cmd[@]}"
        "${go_test_cmd[@]}"
    done
}

run_unit_tests

echo "All unit tests passed successfully."
