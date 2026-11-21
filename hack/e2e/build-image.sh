#!/usr/bin/env bash
# Builds an edge image of a kuberoot checkout on a machine with Docker,
# in the same containers as the builder pod (hack/hetzner/builder.yaml): the
# kernel and DRBD unless <work>/out/<arch> already has them, the binaries, the
# root filesystem, the initramfs and, with --bundle, a signed release bundle.
# The release key is <work>/keys/release.{key,pub}, made if missing.
# KUBEROOT_ARCH names the architecture (amd64 by default); amd64 and arm64
# must be the machine's own, as on GitHub's amd64 and arm64 runners, riscv64
# is cross-built on an amd64 machine.
# (Not ARCH: the kernel's makefiles read that from the environment.)
# Usage: hack/e2e/build-image.sh <checkout> <work> <version> [--bundle]
set -euo pipefail

src=$(cd "${1:?checkout required}" && pwd)
work=${2:?work directory required}
version=${3:?version required}
bundle=${4:-}
arch=${KUBEROOT_ARCH:-amd64}
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
    -e KUBEROOT_VERSION="$version" -e KUBEROOT_DISTRO=edge -e KUBEROOT_ARCH="$arch" \
    -e GOFLAGS="-mod=mod -buildvcs=false" -e GOMODCACHE=/work/gomod -e GOCACHE=/work/gocache \
    "$image" "$@"
}

run "$go_image" bash -euc '
  apt-get -qq update
  apt-get -qq install -y flex bison bc libelf-dev libssl-dev cpio xz-utils zstd >/dev/null
  if [ -f /work/out/$KUBEROOT_ARCH/vmlinuz.efi ] && [ -d /work/out/$KUBEROOT_ARCH/modules ]; then
    echo "kernel: kept $(cat /work/out/$KUBEROOT_ARCH/kernel.flavour) from the cache"
  else
    scripts/build-kernel.sh $KUBEROOT_ARCH
    scripts/build-drbd.sh $KUBEROOT_ARCH
  fi
  scripts/build-binaries.sh $KUBEROOT_ARCH'
run "$alpine_image" sh -c 'sh /src/scripts/build-rootfs.sh $KUBEROOT_ARCH'
run "$go_image" bash -euc '
  apt-get -qq update && apt-get -qq install -y cpio >/dev/null
  scripts/build-initramfs.sh $KUBEROOT_ARCH
  if [ "$1" = --bundle ]; then scripts/build-bundle.sh $KUBEROOT_ARCH; fi' _ "$bundle"
