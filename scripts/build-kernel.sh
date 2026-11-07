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
# rpi.config rides with arm64 rather than being asked for: one arm64 kernel
# that boots a virtual machine and a board alike cannot drift from itself, and
# the board drivers cost a fraction of an image already measured in tens of
# megabytes. The alternative -- two arm64 kernels -- means every later change
# has to be made twice and verified twice, and the one nobody tested is the one
# that ends up on the hardware.
if [ "$ARCH" = arm64 ]; then set -- "$@" rpi.config; fi
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
# that misbehaves; these break networking, storage or guest resource growth
# long after the image was built, so check their final values here instead.
for opt in CONFIG_DEBUG_INFO_BTF CONFIG_BLK_DEV_DM CONFIG_HOTPLUG_CPU \
  CONFIG_ACPI_PROCESSOR CONFIG_ACPI_HOTPLUG_CPU CONFIG_MEMORY_HOTPLUG \
  CONFIG_MEMORY_HOTREMOVE CONFIG_CONTIG_ALLOC CONFIG_EXCLUSIVE_SYSTEM_RAM \
  CONFIG_VIRTIO_MEM CONFIG_MHP_DEFAULT_ONLINE_TYPE_ONLINE_AUTO; do
  grep -qx "$opt=y" "$O/.config" || {
    echo "$opt was asked for in the fragments and is not in the resulting config: a dependency of it is unmet" >&2
    exit 1
  }
done

# A Raspberry Pi is described by a devicetree, not by ACPI, and its firmware
# loads a plain Image -- it knows nothing of the EFI zboot wrapper the virtual
# machines boot from. Both are asked for here rather than in a second pass, so
# one kernel serves both ways of starting and the two can never drift apart.
targets="$(basename $IMAGE) modules"
[ "$ARCH" = arm64 ] && targets="$targets Image dtbs"
# shellcheck disable=SC2086 # deliberately word-split: these are make targets
make -s ARCH=$KARCH O="$O" -j"${JOBS:-$(nproc)}" $targets
grep -q "=m$" "$O/.config" && { echo "modules left in the kernel config:" >&2; grep "=m$" "$O/.config" >&2; exit 1; }

cp "$O/$IMAGE" "$OUT/vmlinuz.efi"
cp "$O/.config" "$OUT/kernel.config"
# The boards this tree is built for, by the name their firmware looks for. A
# missing one fails here rather than on the board, where the only symptom is a
# rainbow screen and no console output at all.
if [ "$ARCH" = arm64 ]; then
  cp "$O/arch/arm64/boot/Image" "$OUT/Image"
  mkdir -p "$OUT/dtb"
  for dtb in bcm2711-rpi-4-b bcm2712-rpi-5-b; do
    src="$O/arch/arm64/boot/dts/broadcom/$dtb.dtb"
    [ -f "$src" ] || { echo "devicetree $dtb.dtb was not built" >&2; exit 1; }
    cp "$src" "$OUT/dtb/$dtb.dtb"
  done
  echo "devicetrees: $(ls "$OUT/dtb" | tr '\n' ' ')"
fi
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
