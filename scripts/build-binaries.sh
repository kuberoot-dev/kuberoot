#!/usr/bin/env bash
# Builds everything the root filesystem carries that is not downloaded: the
# kuberoot binaries, the pinned kubepkg, the glibc runtime the upstream kubelet
# links against, the release key, and fetches systemd-boot. Runs in the builder.
# Usage: scripts/build-binaries.sh <arch: amd64|arm64>
set -euo pipefail

ARCH=${1:?arch required}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=${WORK:-/work}
OUT=${KUBEROOT_OUT:-$ROOT/out}/$ARCH
mkdir -p "$OUT"

for cmd in kinit kuberoot-node kuberoot-installer kuberoot-intents kuberoot-router kuberoot-vmctl kuberoot-aictl kuberoot-agent kuberoot-devices kuberoot-wsctl; do
  (cd "$ROOT" && GOOS=linux GOARCH=$ARCH CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$OUT/$cmd" "./cmd/$cmd")
done
"$ROOT/scripts/build-kubepkg.sh" "$ARCH"

# The upstream kubelet is linked against glibc: carry just its runtime.
rm -rf "$OUT/glibc"
multiarch=$(ls -d /lib/*-linux-gnu | head -1)
mkdir -p "$OUT/glibc$multiarch"
for l in libc.so.6 libresolv.so.2 libpthread.so.0; do cp -L "$multiarch/$l" "$OUT/glibc$multiarch/"; done
for loader in /lib/ld-linux-*.so.* /lib64/ld-linux-*.so.*; do [ -e "$loader" ] && break; done
mkdir -p "$OUT/glibc$(dirname "$loader")"
cp -L "$loader" "$OUT/glibc$loader"

# Release bundles must be signed with this key; a development key is made once.
KEYS=${KUBEROOT_KEYS:-$WORK/keys}
mkdir -p "$KEYS"
if [ ! -f "$KEYS/release.pub" ]; then
  (cd "$ROOT" && go run ./cmd/kuberoot-release keygen "$KEYS/release")
fi
cp "$KEYS/release.pub" "$OUT/release.pub"

# systemd-boot from the distribution's package.
tmp=$(mktemp -d)
(cd "$tmp" && apt-get -qq download systemd-boot-efi && dpkg-deb -x systemd-boot-efi_*.deb x)
efi=$(ls "$tmp"/x/usr/lib/systemd/boot/efi/systemd-boot*.efi | head -1)
cp "$efi" "$OUT/systemd-boot.efi"
rm -rf "$tmp"
echo "binaries: $OUT"
