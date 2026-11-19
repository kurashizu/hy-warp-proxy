#!/usr/bin/env bash
# Rebuild hygw for every supported platform (needs Go >= 1.24, internet for modules)
set -euo pipefail
cd "$(dirname "$0")"
OUT=${1:-dist}; mkdir -p "$OUT"
for t in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  os=${t%/*}; arch=${t#*/}; ext=; [ $os = windows ] && ext=.exe
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=${VERSION:-1.0.0}" -o "$OUT/hygw-$os-$arch$ext" .
  echo "built $OUT/hygw-$os-$arch$ext"
done
