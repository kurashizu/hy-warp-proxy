#!/usr/bin/env bash
# Build the static userspace WireGuard (used when the VPS kernel has no WireGuard module). Needs Go >= 1.23.
set -e
git clone --depth 1 https://github.com/WireGuard/wireguard-go wgo && cd wgo
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o ../wireguard-go .
