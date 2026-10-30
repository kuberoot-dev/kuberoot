#!/usr/bin/env bash
# Builds the kuberoot kernel. Runs in the builder (hack/hetzner.sh), natively or
# with a cross compiler; sources and objects stay in $WORK between builds. A
# distribution with a kernel fragment of its own (distros/<name>/kernel.config,
# picked by KUBEROOT_DISTRO) gets a kernel of its own, built apart.
# JOBS bounds the compilers run at once; all CPUs by default.
# Usage: scripts/build-kernel.sh <arch: amd64|arm64> [extra fragment...]
set -euo pipefail

ARCH=${1:?arch required}
shift
ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=${WORK:-/work}
OUT=${KUBEROOT_OUT:-$ROOT/out}/$ARCH
KVER=$(cat "$ROOT/kernel/VERSION")
. "$ROOT/scripts/kernel-flavour.sh"

case "$ARCH" in
  amd64) KARCH=x86_64; IMAGE=arch/x86/boot/bzImage ;;
  arm64) KARCH=arm64; IMAGE=arch/arm64/boot/vmlinuz.efi ;;
  *) echo "unknown arch $ARCH" >&2; exit 1 ;;
esac

mkdir -p "$WORK/src" "$OUT"
cd "$WORK/src"
if [ ! -d "linux-$KVER" ]; then
  curl -fsSL "https://cdn.kernel.org/pub/linux/kernel/v${KVER%%.*}.x/linux-$KVER.tar.xz" | tar -xJ
fi
cd "linux-$KVER"
O=$(kernel_build_dir "$WORK" "$KARCH")
kernel_cross "$KARCH"
# The arm64 EFI image pads the kernel to its size in memory, read with hexdump.
[ "$ARCH" != arm64 ] || command -v hexdump >/dev/null || { apt-get -qq update && apt-get -qq install -y bsdextrautils >/dev/null; }
# CONFIG_DEBUG_INFO_BTF turns the kernel's debug information into the type
# information eBPF programs read at load time, and pahole is what does the
# turning. Without it the build stops at the BTF step rather than quietly
# producing a kernel Cilium cannot load.
command -v pahole >/dev/null || { apt-get -qq update && apt-get -qq install -y dwarves >/dev/null; }
make -s ARCH=$KARCH O="$O" defconfig
frags="$ROOT/kernel/base.config"
for f in "$ARCH.config" "$@"; do [ -f "$ROOT/kernel/$f" ] && frags="$frags $ROOT/kernel/$f"; done
[ -z "$KFLAVOUR" ] || frags="$frags $ROOT/distros/$KFLAVOUR/kernel.config"
KCONFIG_CONFIG="$O/.config" scripts/kconfig/merge_config.sh -m -O "$O" "$O/.config" $frags >/dev/null
make -s ARCH=$KARCH O="$O" olddefconfig
# Modules are on only for out-of-tree drivers: everything defconfig would
# build as a module is built in, as it was with modules off.
sed -i 's/=m$/=y/' "$O/.config"
make -s ARCH=$KARCH O="$O" olddefconfig
# olddefconfig drops an option whose dependencies the fragments did not also
# ask for, and says nothing. For most options the loss shows up as a kernel
# that misbehaves; for these it shows up much later, as a CNI that refuses to
# load on a cluster that is already built, so check them here instead.
for opt in CONFIG_DEBUG_INFO_BTF CONFIG_BLK_DEV_DM; do
  grep -qx "$opt=y" "$O/.config" || {
    echo "$opt was asked for in the fragments and is not in the resulting config: a dependency of it is unmet" >&2
    exit 1
  }
done

make -s ARCH=$KARCH O="$O" -j"${JOBS:-$(nproc)}" "$(basename $IMAGE)" modules
grep -q "=m$" "$O/.config" && { echo "modules left in the kernel config:" >&2; grep "=m$" "$O/.config" >&2; exit 1; }

cp "$O/$IMAGE" "$OUT/vmlinuz.efi"
cp "$O/.config" "$OUT/kernel.config"
echo "${KFLAVOUR:-base}" > "$OUT/kernel.flavour"
# An arm64 kernel takes either the boot loader's arguments or its own, never
# both: the flavour's own arguments travel next to the kernel instead, as
# kernel.args, and go into its boot entries.
rm -f "$OUT/kernel.args"
if [ "$ARCH" = arm64 ]; then
  args=$(sed -n 's/^CONFIG_CMDLINE="\(.*\)"$/\1/p' "$O/.config")
  [ -z "$args" ] || echo "$args" > "$OUT/kernel.args"
fi
echo "kernel: $OUT/vmlinuz.efi ($(du -h "$OUT/vmlinuz.efi" | cut -f1))"
