#!/bin/sh
# Reproducible builds: the same source and Go version give byte-identical files (no paths, no build id, no cgo).
# Check: build twice (or on another machine with the same Go version) and compare dist/SHA256SUMS.
set -e
cd "$(dirname "$0")"
GO=${GO:-go}
mkdir -p dist
for t in windows/amd64 darwin/arm64 darwin/amd64 linux/amd64; do
  os=${t%/*}; arch=${t#*/}; ext=""; [ "$os" = windows ] && ext=.exe
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch $GO build -trimpath -buildvcs=false -ldflags "-s -w -buildid=" -o "dist/stock-client-$os-$arch$ext" .
done
(cd dist && sha256sum stock-client-* > SHA256SUMS)
$GO version > dist/GO_VERSION
cat dist/SHA256SUMS
