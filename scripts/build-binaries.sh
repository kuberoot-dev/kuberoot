#!/usr/bin/env bash
# Builds everything the root filesystem carries that is not downloaded: the
# kuberoot binaries, the pinned kubepkg, the glibc runtime the upstream kubelet
# links against, the release key, and fetches systemd-boot. Runs in the builder.
# riscv64 is a single node for emulators: only what the edge distribution
# runs there, Kubernetes built from source (build-kubernetes.sh) and the
# pause program, as upstream publishes neither for it.
# Usage: scripts/build-binaries.sh <arch: amd64|arm64|riscv64>
set -euo pipefail

ARCH=${1:?arch required}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=${WORK:-/work}
OUT=${KUBEROOT_OUT:-$ROOT/out}/$ARCH
mkdir -p "$OUT"

cmds="kinit kuberoot-node kuberoot-installer kuberoot-intents kuberoot-fleet"
case "$ARCH" in
  riscv64) cmds="$cmds kuberoot-pause" ;;
  *) cmds="$cmds kuberoot-router kuberoot-vmctl kuberoot-aictl kuberoot-agent kuberoot-devices kuberoot-wsctl" ;;
esac
for cmd in $cmds; do
  (cd "$ROOT" && GOOS=linux GOARCH=$ARCH CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$OUT/$cmd" "./cmd/$cmd")
done
# The resource store lab (docs/resource-store.md), only when asked for: it
# can run in etcd's place (kuberoot.datastore=kuberoot-store), never by default.
rm -f "$OUT/kuberoot-store"
if [ "${KUBEROOT_STORE_LAB:-}" = 1 ]; then
  (cd "$ROOT" && GOOS=linux GOARCH=$ARCH CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$OUT/kuberoot-store" ./cmd/kuberoot-store)
fi
"$ROOT/scripts/build-kubepkg.sh" "$ARCH"
[ "$ARCH" != riscv64 ] || "$ROOT/scripts/build-kubernetes.sh" "$ARCH"

# The upstream kubelet is linked against glibc: carry just its runtime. The
# builder's own for its architecture; another's from Debian's package of it.
# foreign <package>: a Debian package for $ARCH, unpacked into a temporary root.
foreign() {
  # Debian has riscv64 from trixie on: its packages come from there.
  if [ "$ARCH" = riscv64 ] && [ ! -f /etc/apt/sources.list.d/kuberoot-riscv64.list ]; then
    echo "deb [arch=riscv64] http://deb.debian.org/debian trixie main" >/etc/apt/sources.list.d/kuberoot-riscv64.list
    dpkg --add-architecture riscv64
    apt-get -qq update >/dev/null
  fi
  dpkg --print-foreign-architectures | grep -qx "$ARCH" || { dpkg --add-architecture "$ARCH" && apt-get -qq update >/dev/null; }
  local d
  d=$(mktemp -d)
  (cd "$d" && apt-get -qq download "$1:$ARCH" >/dev/null && dpkg-deb -x ./*.deb x)
  echo "$d/x"
}
rm -rf "$OUT/glibc"
# riscv64's kubelet is built here, static: it needs no C library.
if [ "$ARCH" != riscv64 ]; then
  sysroot=""
  [ "$(dpkg --print-architecture)" = "$ARCH" ] || sysroot=$(foreign libc6)
  # By the target's triplet: a builder with cross compilers has other
  # architectures' directories under /lib too.
  case "$ARCH" in amd64) multiarch=lib/x86_64-linux-gnu ;; arm64) multiarch=lib/aarch64-linux-gnu ;; *) echo "unknown arch $ARCH" >&2; exit 1 ;; esac
  mkdir -p "$OUT/glibc/$multiarch"
  for l in libc.so.6 libresolv.so.2 libpthread.so.0; do cp -L "$sysroot/$multiarch/$l" "$OUT/glibc/$multiarch/"; done
  case "$ARCH" in amd64) loader=lib64/ld-linux-x86-64.so.2 ;; arm64) loader=lib/ld-linux-aarch64.so.1 ;; esac
  mkdir -p "$OUT/glibc/$(dirname "$loader")"
  (cd "$sysroot/$(dirname "$loader")" && cp -L "$(basename "$loader")" "$OUT/glibc/$loader")
  [ -z "$sysroot" ] || rm -rf "$(dirname "$sysroot")"
fi

# Release bundles must be signed with this key; a development key is made once.
KEYS=${KUBEROOT_KEYS:-$WORK/keys}
mkdir -p "$KEYS"
if [ ! -f "$KEYS/release.pub" ]; then
  (cd "$ROOT" && go run ./cmd/kuberoot-release keygen "$KEYS/release")
fi
cp "$KEYS/release.pub" "$OUT/release.pub"

# systemd-boot from the distribution's package, for the target's firmware.
if [ "$(dpkg --print-architecture)" = "$ARCH" ]; then
  tmp=$(mktemp -d)
  (cd "$tmp" && apt-get -qq download systemd-boot-efi && dpkg-deb -x systemd-boot-efi_*.deb x)
  tmp="$tmp/x"
else
  tmp=$(foreign systemd-boot-efi)
fi
efi=$(ls "$tmp"/usr/lib/systemd/boot/efi/systemd-boot*.efi | head -1)
cp "$efi" "$OUT/systemd-boot.efi"
rm -rf "$(dirname "$tmp")"
echo "binaries: $OUT"
