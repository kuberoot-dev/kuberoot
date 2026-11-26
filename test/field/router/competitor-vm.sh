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
  [[ -f $d/sys.img ]] || gzip -dc /work/field/images/openwrt-24.10.8.img.gz > "$d/sys.img"
  drive="file=$d/sys.img,format=raw,if=virtio";;
 vyos)
  [[ -f $d/sys.qcow2 ]] || qemu-img create -q -f qcow2 "$d/sys.qcow2" 12G
  drive="file=$d/sys.qcow2,format=qcow2,if=virtio"
  [[ ${INSTALLED:-0} == 1 ]] || extra=(-cdrom /work/field/images/vyos-2026.11.25-0017.iso -boot d);;
 debian)
  [[ -f $d/sys.qcow2 ]] || qemu-img create -q -f qcow2 -F qcow2 -b /work/field/images/debian-12-genericcloud-amd64.qcow2 "$d/sys.qcow2" 12G
  drive="file=$d/sys.qcow2,format=qcow2,if=virtio"
  [[ ! -f $d/seed.img ]] || extra=(-drive "file=$d/seed.img,format=raw,if=virtio,readonly=on");;
esac
# Preparatory network only. No performance claim may use this user-net WAN.
qemu-system-x86_64 -name "$name" -enable-kvm -cpu host -smp 2 -m 2048 -machine q35 \
 -drive if=pflash,format=raw,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.fd \
 -drive "if=pflash,format=raw,file=$d/vars.fd" -drive "$drive" "${extra[@]}" \
 -netdev user,id=wan,hostfwd=tcp:127.0.0.1:2222-:22 \
 -device virtio-net-pci,netdev=wan,mac=52:54:00:30:00:01 \
 -netdev socket,id=lan,mcast=230.0.0.5:12005,localaddr=127.0.0.1 \
 -device virtio-net-pci,netdev=lan,mac=52:54:00:30:00:02 \
 -chardev "socket,id=ser,path=$d/serial.sock,server=on,wait=off,logfile=$d/console.log,logappend=on" \
 -serial chardev:ser -qmp "unix:$d/qmp.sock,server=on,wait=off" \
 -display none -daemonize -pidfile "$d/pid"
