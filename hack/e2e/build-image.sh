#!/usr/bin/env bash
# Builds an amd64 edge image of a kuberoot checkout on a machine with Docker,
# in the same containers as the builder pod (hack/hetzner/builder.yaml): the
# kernel and DRBD unless <work>/out/amd64 already has them, the binaries, the
# root filesystem, the initramfs and, with --bundle, a signed release bundle.
# The release key is <work>/keys/release.{key,pub}, made if missing.
# Usage: hack/e2e/build-image.sh <checkout> <work> <version> [--bundle]
set -euo pipefail

src=$(cd "${1:?checkout required}" && pwd)
work=${2:?work directory required}
version=${3:?version required}
bundle=${4:-}
mkdir -p "$work"
work=$(cd "$work" && pwd)

go_image=golang:$(sed -n 's/^go \([0-9]*\.[0-9]*\).*/\1/p' "$src/go.mod")-bookworm
alpine_image=alpine:$(sed -n 's/^ALPINE_VERSION=//p' "$src/build/components.env")

# Downloads from upstream fail now and then (504s); every curl of the build
# steps retries, without changing the steps themselves.
printf 'retry = 15\nretry-all-errors\nretry-delay = 20\n' >"$work/.curlrc"

run() {
  local image=$1
  shift
  docker run --rm -v "$src:/src" -v "$work:/work" -w /src \
    -e CURL_HOME=/work -e WORK=/work -e KUBEROOT_OUT=/work/out -e KUBEROOT_KEYS=/work/keys \
    -e KUBEROOT_VERSION="$version" -e KUBEROOT_DISTRO=edge \
    -e GOFLAGS="-mod=mod -buildvcs=false" -e GOMODCACHE=/work/gomod -e GOCACHE=/work/gocache \
    "$image" "$@"
}

run "$go_image" bash -euc '
  apt-get -qq update
  apt-get -qq install -y flex bison bc libelf-dev libssl-dev cpio xz-utils zstd >/dev/null
  if [ -f /work/out/amd64/vmlinuz.efi ] && [ -d /work/out/amd64/modules ]; then
    echo "kernel: kept $(cat /work/out/amd64/kernel.flavour) from the cache"
  else
    scripts/build-kernel.sh amd64
    scripts/build-drbd.sh amd64
  fi
  scripts/build-binaries.sh amd64'
run "$alpine_image" sh /src/scripts/build-rootfs.sh amd64
run "$go_image" bash -euc '
  apt-get -qq update && apt-get -qq install -y cpio >/dev/null
  scripts/build-initramfs.sh amd64
  if [ "$1" = --bundle ]; then scripts/build-bundle.sh amd64; fi' _ "$bundle"
