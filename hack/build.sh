#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
mkdir -p "${project_root}/bin"
go build -o "${project_root}/bin/etcd-infra" "${project_root}/cmd/etcd-infra"
