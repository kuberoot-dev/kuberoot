#!/usr/bin/env bash
# Builds DRBD 9 against the kernel build-kernel.sh made and signs it with that
# build's module key; the kernel accepts no other modules. Runs in the builder.
# Usage: scripts/build-drbd.sh <arch: amd64|arm64>
set -euo pipefail

ARCH=${1:?arch required}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=${WORK:-/work}
OUT=${KUBEROOT_OUT:-$ROOT/out}/$ARCH
DRBD_VERSION=$(cat "$ROOT/kernel/DRBD_VERSION")
case "$ARCH" in amd64) KARCH=x86_64 ;; arm64) KARCH=arm64 ;; *) echo "unknown arch $ARCH" >&2; exit 1 ;; esac
. "$ROOT/scripts/kernel-flavour.sh"
O=$(kernel_build_dir "$WORK" "$KARCH")
KREL=$(cat "$O/include/config/kernel.release")
kernel_cross "$KARCH"

# DRBD adapts to the kernel with coccinelle patches.
command -v spatch >/dev/null && command -v patch >/dev/null || { apt-get -qq update && apt-get -qq install -y coccinelle patch >/dev/null; }

mkdir -p "$WORK/src"
cd "$WORK/src"
if [ ! -d "drbd-$DRBD_VERSION" ]; then
  curl -fsSL "https://pkg.linbit.com/downloads/drbd/9/drbd-$DRBD_VERSION.tar.gz" | tar -xz
fi
cd "drbd-$DRBD_VERSION"
make -s clean >/dev/null 2>&1 || true
make -s -j"${JOBS:-$(nproc)}" KDIR="$O" ARCH=$KARCH ${CROSS_COMPILE:+CROSS_COMPILE=$CROSS_COMPILE} module

dst="$OUT/modules/$KREL/extra"
rm -rf "$OUT/modules"
mkdir -p "$dst"
for m in drbd/build-current/drbd.ko drbd/build-current/drbd_transport_tcp.ko; do
  [ -f "$m" ] || m=$(find . -name "$(basename "$m")" | head -1)
  "$O/scripts/sign-file" sha512 "$O/certs/signing_key.pem" "$O/certs/signing_key.x509" "$m" "$dst/$(basename "$m")"
done
echo "drbd $DRBD_VERSION for $KREL: $(ls "$dst" | tr '\n' ' ')"
