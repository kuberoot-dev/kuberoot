#!/bin/bash
# Exactly one competitor VM at a time, within the existing pod's allocation.
set -euo pipefail
name=${1:?openwrt, vyos or debian}
case "$name" in openwrt|vyos|debian);; *) exit 2;; esac
for other in openwrt vyos debian; do
 if [[ -f /work/vms/$other/pid ]] && kill -0 "$(cat /work/vms/$other/pid)" 2>/dev/null; then
  echo "competitor already running: $other" >&2; exit 1
 fi
done
d=/work/vms/$name;mkdir -p "$d"
[[ -f $d/vars.fd ]] || cp /usr/share/OVMF/OVMF_VARS_4M.fd "$d/vars.fd"
extra=()
case "$name" in
 openwrt)
  if [[ ! -f $d/sys.img ]]; then
   # Official OpenWrt gzip has trailing metadata. GNU gzip status 2 is a
   # warning after successful decompression; status 1 is fatal.
   status=0
   gzip -dc /work/field/images/openwrt-24.10.8.img.gz > "$d/sys.img.partial" || status=$?
   [[ $status == 0 || $status == 2 ]] || exit "$status"
   qemu-img info -f raw "$d/sys.img.partial" >/dev/null
   mv "$d/sys.img.partial" "$d/sys.img"
  fi
  drive="file=$d/sys.img,format=raw,if=none,id=system,cache=writeback";;  # image size is not 4 KiB aligned, O_DIRECT refuses it
 vyos)
  [[ -f $d/sys.qcow2 ]] || qemu-img create -q -f qcow2 "$d/sys.qcow2" 12G
  drive="file=$d/sys.qcow2,format=qcow2,if=none,id=system,cache=none"
  [[ ${INSTALLED:-0} == 1 ]] || extra=(-cdrom /work/field/images/vyos-2026.11.25-0017.iso -boot d);;
 debian)
  [[ -f $d/sys.qcow2 ]] || qemu-img create -q -f qcow2 -F qcow2 -b /work/field/images/debian-12-genericcloud-amd64.qcow2 "$d/sys.qcow2" 12G
  drive="file=$d/sys.qcow2,format=qcow2,if=none,id=system,cache=none"
  [[ ! -f $d/seed.img ]] || extra=(-drive "file=$d/seed.img,format=raw,if=virtio,readonly=on");;
esac
# Equal userspace socket fabric; third NIC is isolated observation only.
# FIELD_EXTRA_NET adds QEMU arguments, e.g. a NIC on the transit segment for R9.
extra_net=(); [[ -z ${FIELD_EXTRA_NET:-} ]] || read -ra extra_net <<< "$FIELD_EXTRA_NET"
qemu-system-x86_64 -name "$name" -enable-kvm -cpu host -smp "${DUT_VCPU:-2}" -m "${DUT_RAM_MIB:-2048}" -machine q35 -boot order=c \
 -drive if=pflash,format=raw,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.fd \
 -drive "if=pflash,format=raw,file=$d/vars.fd" -drive "$drive" -device virtio-blk-pci,drive=system,bootindex=1 "${extra[@]}" \
 -netdev socket,id=wan,mcast=230.31.0.32:13032,localaddr=127.0.0.1 \
 -device virtio-net-pci,romfile=,netdev=wan,mac=52:54:00:30:00:01 \
 -netdev socket,id=lan,mcast=230.31.0.31:13031,localaddr=127.0.0.1 \
 -device virtio-net-pci,romfile=,netdev=lan,mac=52:54:00:30:00:02 \
 -netdev user,id=observer,hostfwd=tcp:127.0.0.1:2222-:22 \
 -device virtio-net-pci,romfile=,netdev=observer,mac=52:54:00:30:00:03 \
 -chardev "socket,id=ser,path=$d/serial.sock,server=on,wait=off,logfile=$d/console.log,logappend=on" \
 -serial chardev:ser -qmp "unix:$d/qmp.sock,server=on,wait=off" \
 "${extra_net[@]}" -display none -daemonize -pidfile "$d/pid"
