#!/usr/bin/env bash
# Build the personal account-value variant without changing generated source files.
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
version="${VERSION:-1.13.2-account-value.1}"
build_dir="$(mktemp -d)"
trap 'rm -rf "$build_dir"' EXIT
cd "$repo_root"
npm run build
cp -R apps/manager-server "$build_dir/server"
cp apps/web/dist/index.html "$build_dir/server/internal/httpapi/web/management.html"
mkdir -p "$repo_root/dist/account-value"
cd "$build_dir/server"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -X github.com/seakee/cpa-manager-plus/apps/manager-server/internal/buildinfo.Version=$version" -o "$repo_root/dist/account-value/cpa-manager-plus" ./cmd/cpa-manager-plus
