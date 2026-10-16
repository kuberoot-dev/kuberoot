#!/usr/bin/env bash
# Builds the NVIDIA driver into the image: the open kernel modules against
# the kernel build-kernel.sh made, signed with its module key as the kernel
# accepts no other, and the user space of the same release (the CUDA driver
# library, NVML, nvidia-smi and the GSP firmware the modules load). Runs in
# the builder; the ai distribution's extras.sh puts the tree in its image.
# The driver is shipped for amd64 only: on arm64 nothing is built, and the
# image's GPUs stay unused (pkg/gpu says so to whoever asks for them).
# Usage: scripts/build-nvidia.sh <arch: amd64|arm64>
set -euo pipefail

ARCH=${1:?arch required}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=${WORK:-/work}
OUT=${KUBEROOT_OUT:-$ROOT/out}/$ARCH
. "$ROOT/build/components.env"
V=$NVIDIA_DRIVER_VERSION
case "$ARCH" in
  amd64) KARCH=x86_64; NARCH=x86_64 ;;
  arm64) rm -rf "$OUT/nvidia"; echo "nvidia: the driver is shipped for amd64 only, the $ARCH image has no GPU support"; exit 0 ;;
  *) echo "unknown arch $ARCH" >&2; exit 1 ;;
esac
. "$ROOT/scripts/kernel-flavour.sh"
O=$(kernel_build_dir "$WORK" "$KARCH")
KREL=$(cat "$O/include/config/kernel.release")
KVER=$(cat "$ROOT/kernel/VERSION")
jobs=${JOBS:-$(nproc)}; [ "$jobs" -le 8 ] || jobs=8

mkdir -p "$WORK/src"
src="$WORK/src/open-gpu-kernel-modules-$V"
[ -d "$src" ] || curl -fsSL "https://github.com/NVIDIA/open-gpu-kernel-modules/archive/refs/tags/$V.tar.gz" | tar -xz -C "$WORK/src"
# The kernel's sources are where build-kernel.sh unpacked them; its objects
# are in O.
ksrc="$WORK/src/linux-$KVER"
make -s -C "$src" -j"$jobs" modules ARCH=$KARCH SYSSRC="$ksrc" SYSOUT="$O" TARGET_ARCH=$NARCH

# The user space: from the installer of the same release, unpacked, not run.
run="$WORK/cache/NVIDIA-Linux-$NARCH-$V.run"
[ -f "$run" ] || curl -fsSL -o "$run" "https://download.nvidia.com/XFree86/Linux-$NARCH/$V/NVIDIA-Linux-$NARCH-$V.run"
x="$WORK/src/nvidia-userspace-$NARCH-$V"
[ -d "$x" ] || sh "$run" --extract-only --target "$x" >/dev/null
# The libraries go beside the glibc runtime build-binaries.sh carries, where
# its loader looks without a cache.
libdir=/lib/x86_64-linux-gnu; sysroot=/
tree="$OUT/nvidia"
rm -rf "$tree"
mkdir -p "$tree/usr/bin" "$tree$libdir" "$tree/lib/firmware/nvidia/$V"
install -m 0755 "$x/nvidia-smi" "$tree/usr/bin/nvidia-smi"
for lib in libcuda libnvidia-ml libnvidia-ptxjitcompiler libnvidia-nvvm libnvidia-gpucomp; do
  [ -f "$x/$lib.so.$V" ] || continue
  install -m 0755 "$x/$lib.so.$V" "$tree$libdir/"
  ln -sf "$lib.so.$V" "$tree$libdir/$lib.so.1"
  ln -sf "$lib.so.1" "$tree$libdir/$lib.so"
done
install -m 0644 "$x"/firmware/gsp_*.bin "$tree/lib/firmware/nvidia/$V/"
# What they link against beyond the runtime already there.
command -v objdump >/dev/null || { apt-get -qq update && apt-get -qq install -y binutils >/dev/null; }
for f in "$tree/usr/bin/nvidia-smi" "$tree$libdir"/*.so.$V; do
  for need in $(objdump -p "$f" | awk '/NEEDED/{print $2}'); do
    case "$need" in libc.so.6|libresolv.so.2|libpthread.so.0|ld-linux*|libnvidia*|libcuda*) continue ;; esac
    [ -e "$tree$libdir/$need" ] && continue
    cp -L "$sysroot$libdir/$need" "$tree$libdir/" 2>/dev/null || cp -L "$sysroot/lib/$need" "$tree$libdir/"
  done
done

# The CDI hook: containers given the driver's libraries need their loader's
# cache refreshed to find them; the device plugin's CDI descriptions call it.
# It links NVML's loader with cgo, against the glibc the image carries.
GOBIN="$tree/usr/bin" CGO_ENABLED=1 GOFLAGS=-mod=mod \
  go install "github.com/NVIDIA/nvidia-container-toolkit/cmd/nvidia-cdi-hook@$NVIDIA_CONTAINER_TOOLKIT_VERSION" 2>/dev/null

# nvidia drives the GPU, nvidia-uvm is the memory CUDA needs, nvidia-modeset
# what the display side links against. nvidia-drm and peermem stay out: no
# display, no RDMA.
dst="$tree/lib/modules/$KREL/extra"
mkdir -p "$dst"
for m in nvidia nvidia-uvm nvidia-modeset; do
  "$O/scripts/sign-file" sha512 "$O/certs/signing_key.pem" "$O/certs/signing_key.x509" \
    "$src/kernel-open/$m.ko" "$dst/$(echo $m | tr - _).ko"
done

echo "nvidia $V for $KREL: $(ls "$dst" | tr '\n' ' ')in $tree, $(du -sh "$tree" | cut -f1)"
