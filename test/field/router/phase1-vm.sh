#!/bin/bash
set -euo pipefail
V=/work/vms
mkdir -p "$V"
up_router() {
 local i=$1 media=${2:-} d=$V/r$1
 mkdir -p "$d"
 [ -f "$d/sys.qcow2" ] || qemu-img create -q -f qcow2 "$d/sys.qcow2" 12G
 [ -f "$d/vars.fd" ] || cp /usr/share/OVMF/OVMF_VARS_4M.fd "$d/vars.fd"
 local drives=()
 [ -z "$media" ] || drives+=(-drive "file=$media,format=raw,if=virtio,snapshot=on")
 qemu-system-x86_64 -name r$i -enable-kvm -cpu host -smp 2 -m "${ROUTER_RAM_MIB:-2048}" -machine q35 \
 -drive if=pflash,format=raw,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.fd \
 -drive if=pflash,format=raw,file="$d/vars.fd" "${drives[@]}" \
 -drive "file=$d/sys.qcow2,format=qcow2,if=virtio" \
 -netdev user,id=wan,hostfwd=tcp:127.0.0.1:50${i}0-:50000,hostfwd=tcp:127.0.0.1:64${i}3-:6443 \
 -device virtio-net-pci,netdev=wan,mac=52:54:00:00:0$i:01 \
 -netdev socket,id=lan,mcast=230.0.0.$i:1200$i,localaddr=127.0.0.1 \
 -device virtio-net-pci,netdev=lan,mac=52:54:00:01:0$i:01 \
 -netdev socket,id=transit,mcast=230.0.0.3:12003,localaddr=127.0.0.1 \
 -device virtio-net-pci,netdev=transit,mac=52:54:00:02:0$i:01 \
 -chardev socket,id=ser,path="$d/serial.sock",server=on,wait=off,logfile="$d/console.log",logappend=on \
 -serial chardev:ser -monitor unix:"$d/monitor.sock",server=on,wait=off \
 -qmp unix:"$d/qmp.sock",server=on,wait=off -display none -daemonize -pidfile "$d/pid"
}
up_client() {
 local i=$1 d=$V/c$1
 mkdir -p "$d"
 [ -f "$d/sys.qcow2" ] || qemu-img create -q -f qcow2 -F qcow2 -b /work/cirros.img "$d/sys.qcow2"
 qemu-system-x86_64 -name c$i -enable-kvm -cpu host -smp 1 -m 256 \
 -drive file="$d/sys.qcow2",format=qcow2,if=virtio \
 -netdev socket,id=lan,mcast=230.0.0.$i:1200$i,localaddr=127.0.0.1 \
 -device virtio-net-pci,netdev=lan,mac=52:54:00:10:0$i:01 \
 -chardev socket,id=ser,path="$d/serial.sock",server=on,wait=off,logfile="$d/console.log",logappend=on \
 -serial chardev:ser -qmp unix:"$d/qmp.sock",server=on,wait=off -display none -daemonize -pidfile "$d/pid"
}
case "$1" in router) up_router "$2" "${3:-}";; client) up_client "$2";; stop) kill "$(cat "$V/$2/pid")";; esac
