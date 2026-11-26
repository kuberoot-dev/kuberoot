#!/bin/bash
# Execute only inside kr-router-stand, never on the workstation.
set -euo pipefail
[[ -d /work/vms && -c /dev/kvm ]] || exit 1
export TMPDIR=/work/tmp
mkdir -p /work/field/{images,raw,casts,img}
apt-get update -qq
DEBIAN_FRONTEND=noninteractive apt-get install -y -qq cloud-image-utils python3-matplotlib iperf3 vde2 > /work/field/raw/packages.log
cd /work/field/images
fetch() { [[ -s $2 ]] || curl -fL --retry 3 "$1" -o "$2.partial" && { [[ -s $2 ]] || mv "$2.partial" "$2"; }; }
fetch https://downloads.openwrt.org/releases/24.10.8/targets/x86/64/openwrt-24.10.8-x86-64-generic-squashfs-combined-efi.img.gz openwrt-24.10.8.img.gz
fetch https://downloads.openwrt.org/releases/24.10.8/targets/x86/64/sha256sums openwrt.sha256sums
expected=$(awk '/openwrt-24.10.8-x86-64-generic-squashfs-combined-efi.img.gz$/ {print $1}' openwrt.sha256sums)
[[ -n $expected ]]
printf '%s  %s\n' "$expected" openwrt-24.10.8.img.gz | sha256sum -c -
fetch https://github.com/vyos/vyos-nightly-build/releases/download/2026.11.25-0017-rolling/vyos-2026.11.25-0017-rolling-generic-amd64.iso vyos-2026.11.25-0017.iso
fetch https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-genericcloud-amd64.qcow2 debian-12-genericcloud-amd64.qcow2
sha256sum *.gz *.iso *.qcow2 > /work/field/raw/images.sha256
