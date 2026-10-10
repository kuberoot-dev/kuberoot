#!/usr/bin/env bash
# Builds the pinned kubepkg operator and CLI for the target and collects its CRDs.
# Usage: scripts/build-kubepkg.sh <arch: arm64|amd64>
set -euo pipefail

ARCH=${1:?arch required}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
OUT=${KUBEROOT_OUT:-$ROOT/out}/$ARCH
mkdir -p "$OUT/kubepkg-crds"

cd "$ROOT/build/kubepkg"
for cmd in kubepkg kubepkg-operator; do
  GOOS=linux GOARCH=$ARCH CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$OUT/$cmd" "github.com/kuberoot-dev/kubepkg/cmd/$cmd"
done
src=$(go list -m -f '{{.Dir}}' github.com/kuberoot-dev/kubepkg)
install -m 0644 "$src"/config/crd/kubepkg.dev_*.yaml "$OUT/kubepkg-crds/"
echo "kubepkg: $(go list -m -f '{{.Version}}' github.com/kuberoot-dev/kubepkg) -> out/$ARCH/{kubepkg,kubepkg-operator,kubepkg-crds}"
