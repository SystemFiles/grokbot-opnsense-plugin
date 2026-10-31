#!/bin/sh
# Cross-compile the stdlib-only MCP server and package it for the launcher:
#   bin/opnsense-mcp-<os>-<arch>[.exe]      raw binary (git-ignored, used directly if present)
#   bin/opnsense-mcp-<os>-<arch>[.exe].gz   committed, unpacked by bin/opnsense-mcp on first run
#   bin/SHA256SUMS                          sha256 of each raw binary (launcher verifies)
#   bin/GOVERSION                           toolchain used (builds are reproducible per version)
# Reproducible: CGO off, -trimpath, stripped, empty build id, gzip -n.
set -eu
cd "$(dirname "$0")/.."
targets=${TARGETS:-"linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64"}
sha() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1; }
: > bin/SHA256SUMS.tmp
for t in $targets; do
  os=${t%/*}; arch=${t#*/}; ext=""
  [ "$os" = windows ] && ext=.exe
  name="opnsense-mcp-$os-$arch$ext"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w -buildid=" -o "bin/$name" ./cmd/opnsense-mcp
  gzip -n -9 -c "bin/$name" > "bin/$name.gz"
  echo "$(sha "bin/$name")  $name" >> bin/SHA256SUMS.tmp
  echo "built bin/$name ($(wc -c < "bin/$name" | tr -d ' ') bytes, gz $(wc -c < "bin/$name.gz" | tr -d ' '))"
done
mv bin/SHA256SUMS.tmp bin/SHA256SUMS
go env GOVERSION > bin/GOVERSION
