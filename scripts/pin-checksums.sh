#!/bin/sh
# Copy SHA256SUMS into both plugins' checksums.txt (the shims refuse anything else).
#   scripts/pin-checksums.sh dist/SHA256SUMS 0.1.0
set -eu
sums="${1:?SHA256SUMS}"; ver="${2:?version}"
for p in plugins/claude-code plugins/cursor; do
  { echo "# sha256  asset for v$ver, from the release's SHA256SUMS. The shim refuses a binary that does not match."
    cat "$sums"; } > "$p/scripts/checksums.txt"
  echo "$ver" > "$p/scripts/VERSION"
done
