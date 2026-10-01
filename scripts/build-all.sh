#!/usr/bin/env bash
# Build release binaries for every supported target into dist/ and write
# dist/SHA256SUMS. Works in git-bash on Windows and on Linux/macOS CI.
#
#   scripts/build-all.sh [VERSION]      (default: git describe, else "dev")
#
# Targets = the platforms Hermes itself runs on (see docs/design.md).
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
NAME=hermes-safe-update
TARGETS="windows/amd64 windows/arm64 darwin/arm64 darwin/amd64 linux/amd64 linux/arm64"

rm -rf dist
mkdir -p dist

for t in $TARGETS; do
  os=${t%/*}
  arch=${t#*/}
  ext=""
  [ "$os" = windows ] && ext=".exe"
  out="dist/${NAME}-${os}-${arch}${ext}"
  echo "build $out"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -buildvcs=false \
    -ldflags "-s -w -X main.version=${VERSION}" -o "$out" ./cmd/$NAME
done

# Licence notices travel with the binaries (BSD-3 terms in docs/third-party.md).
cp LICENSE NOTICE docs/third-party.md dist/

# The two Windows launcher shims ship as release assets too (hashed as checked out).
cp scripts/hermes-safe-update.cmd scripts/hermes-update-check.cmd dist/

cd dist
files=$(ls | grep -v '^SHA256SUMS$')
if command -v sha256sum >/dev/null 2>&1; then
  # shellcheck disable=SC2086
  sha256sum -- $files | sed 's/ \*/  /' > SHA256SUMS
else
  # shellcheck disable=SC2086
  shasum -a 256 -- $files > SHA256SUMS
fi
echo "wrote dist/SHA256SUMS (version $VERSION)"
