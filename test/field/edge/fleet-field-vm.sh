#!/bin/bash
# E10 only: center 2 GiB, sites 3 GiB, gateways 512 MiB, retained shared gateway
# 512 MiB = 13 GiB guests. Every site uses its own isolated multicast/NAT VM.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
role=${1:?center|site|gateway};id=${2:?0..3};media=${3:-}
[[ $id =~ ^[0-3]$ ]] || exit 2
D=$FIELD_ROOT/fleet-field/$role-$id
mkdir -p "$D"
ram=3072;[[ $role != center ]] || ram=2048;[[ $role != gateway ]] || ram=512
python3 - "$D" "$ram" <<'PY'
import pathlib,sys
memory=int(sys.argv[2])
for p in pathlib.Path('/proc').glob('[0-9]*/cmdline'):
 try:a=p.read_bytes().decode().split('\0')
 except (OSError,UnicodeError):continue
 if not a[0].endswith('qemu-system-x86_64'):continue
 if any(sys.argv[1]+'/' in x for x in a):raise SystemExit('This fleet VM is already running')
 if '-m' in a:memory+=int(a[a.index('-m')+1])
if memory>13312:raise SystemExit('Guest RAM would exceed 13 GiB')
PY
if [[ $role == gateway ]]; then
 test -f "$D/system.qcow2" || qemu-img create -f qcow2 -F qcow2 -b "$FIELD_ROOT/fleet-field/gateway-base.qcow2" "$D/system.qcow2"
 qemu-system-x86_64 -name "field-fleet-gateway-$id" -enable-kvm -cpu host -smp 1 -m "$ram" -machine q35 \
  -drive "file=$D/system.qcow2,format=qcow2,if=virtio" \
  -drive "file=$FIELD_ROOT/gateway/seed.iso,format=raw,if=virtio,readonly=on" \
  -netdev "user,id=wan,hostfwd=tcp:127.0.0.1:2230$id-:22" \
  -device virtio-net-pci,netdev=wan,mac=52:54:00:78:ff:01 \
  -netdev "socket,id=lan,mcast=230.81.0.$id:1810$id,localaddr=127.0.0.1" \
  -device virtio-net-pci,netdev=lan,mac=52:54:00:78:ff:02 \
  -qmp "unix:$D/qmp.sock,server=on,wait=off" -serial "file:$D/serial.log" -display none -daemonize -pidfile "$D/pid"
 exit
fi
test -f "$D/system.raw" || qemu-img create -f raw "$D/system.raw" 32G
test -f "$D/vars.fd" || cp /usr/share/OVMF/OVMF_VARS_4M.fd "$D/vars.fd"
drives=();test -z "$media" || drives=(-drive "file=$media,format=raw,if=virtio,snapshot=on")
if [[ $role == center ]]; then
 net='user,id=lan,hostfwd=tcp:127.0.0.1:29500-:50000,hostfwd=tcp:127.0.0.1:29443-:6443';mac=52:54:00:81:ff:01
else
 net="socket,id=lan,mcast=230.81.0.$id:1810$id,localaddr=127.0.0.1";mac=52:54:00:81:00:0$id
fi
qemu-system-x86_64 -name "field-fleet-$role-$id" -enable-kvm -cpu host -smp 2 -m "$ram" -machine q35 -smbios type=1,serial=field-edge-fault-test \
 -drive if=pflash,format=raw,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.fd \
 -drive "if=pflash,format=raw,file=$D/vars.fd" "${drives[@]}" \
 -drive "file=$D/system.raw,format=raw,if=virtio,cache=none" \
 -netdev "$net" -device "virtio-net-pci,netdev=lan,mac=$mac" \
 -qmp "unix:$D/qmp.sock,server=on,wait=off" -serial "file:$D/serial.log" -display none -daemonize -pidfile "$D/pid"
