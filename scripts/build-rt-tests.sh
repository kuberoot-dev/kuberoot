#!/usr/bin/env bash
# Builds cyclictest, the latency measurement of rt-tests, as one static
# binary for the rt distribution's latency tests. Runs in the builder.
# Usage: scripts/build-rt-tests.sh <arch: amd64|arm64>
set -euo pipefail

ARCH=${1:?arch required}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=${WORK:-/work}
OUT=${KUBEROOT_OUT:-$ROOT/out}/$ARCH
. "$ROOT/build/components.env"
bin="$OUT/cyclictest-$RT_TESTS_VERSION"
[ -f "$bin" ] && { echo "cyclictest $RT_TESTS_VERSION: $bin (built before)"; exit 0; }

case "$ARCH" in
  amd64) cc=gcc; pkgs=(gcc make libnuma-dev) ;;
  arm64) cc=aarch64-linux-gnu-gcc; pkgs=(gcc-aarch64-linux-gnu make libnuma-dev:arm64) ;;
  *) echo "unknown arch $ARCH" >&2; exit 1 ;;
esac
[ "$ARCH" = amd64 ] || dpkg --print-foreign-architectures | grep -qx "$ARCH" || dpkg --add-architecture "$ARCH"
missing=()
for p in "${pkgs[@]}"; do dpkg -s "$p" >/dev/null 2>&1 || missing+=("$p"); done
[ ${#missing[@]} -eq 0 ] || { apt-get -qq update && apt-get -qq install -y "${missing[@]}" >/dev/null; }

src="$WORK/src/rt-tests-$RT_TESTS_VERSION"
[ -d "$src" ] || curl -fsSL "https://mirrors.edge.kernel.org/pub/linux/utils/rt-tests/rt-tests-$RT_TESTS_VERSION.tar.xz" | tar -xJ -C "$WORK/src"
# Linked whole: the root filesystem carries no libnuma. The objects of a
# build for another architecture go first.
make -s -C "$src" clean >/dev/null
make -s -C "$src" CC="$cc" LDFLAGS=-static cyclictest
install -D -m 0755 "$src/cyclictest" "$bin"
echo "cyclictest $RT_TESTS_VERSION: $bin"
