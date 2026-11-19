#!/bin/sh
# Reproducible release build: same Go version + these flags = same bytes.
#   scripts/build-release.sh 0.1.0 [outdir]
# Writes <outdir>/agentwatch-<ver>-<os>-<arch>[.exe] and <outdir>/SHA256SUMS.
set -eu
ver="${1:?version}"
out="${2:-dist}"
mkdir -p "$out"
pkg=github.com/chinmayNayar/agentwatch-plugins
for t in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64; do
  os=${t%/*}; arch=${t#*/}
  a=$arch; [ "$a" = amd64 ] && a=x64
  ext=""; [ "$os" = windows ] && ext=".exe"
  name="agentwatch-${ver}-${os}-${a}${ext}"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -buildvcs=false \
    -ldflags "-s -w -buildid= -X $pkg/internal/client.Version=$ver -X main.commit=v$ver" \
    -o "$out/$name" ./cmd/agentwatch
done
( cd "$out" && (command -v sha256sum >/dev/null && sha256sum agentwatch-* || shasum -a 256 agentwatch-*) | sed 's/\*//' > SHA256SUMS )
cat "$out/SHA256SUMS"
