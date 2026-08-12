#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
mkdir -p "${project_root}/bin"

case "${ETCD_INFRA_CLIENT:-official}" in
    official) go build -o "${project_root}/bin/etcd-infra" "${project_root}/cmd/etcd-infra" ;;
    custom) go build -modfile="${project_root}/go.custom.mod" -tags=etcd_infra_custom_client -o "${project_root}/bin/etcd-infra" "${project_root}/cmd/etcd-infra" ;;
    *) echo "ETCD_INFRA_CLIENT must be official or custom" >&2; exit 2 ;;
esac
