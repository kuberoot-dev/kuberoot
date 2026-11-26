#!/bin/bash
# Three-node fault fixture. Management user-net cannot carry etcd peer addresses.
# Data NICs share an unprivileged multicast segment; QMP set_link partitions it.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
action=${1:?start|stop|link}; id=${2:?1..3}
[[ $id =~ ^[123]$ ]] || exit 2
: "${HA_ROOT:?owned fixture directory}"
[[ $HA_ROOT == /work/field-edge/ha-* ]] || exit 2
d=$HA_ROOT/n$id
case "$action" in
start)
 require_stand
 python3 - "$d" <<'PY'
import pathlib,sys
memory=4096
for p in pathlib.Path('/proc').glob('[0-9]*/cmdline'):
 try:a=p.read_bytes().decode().split('\0')
 except (OSError,UnicodeError):continue
 if not a[0].endswith('qemu-system-x86_64'):continue
 if any(str(pathlib.Path(sys.argv[1]))+'/' in x for x in a):raise SystemExit('fixture node is already running')
 if '-m' in a:memory+=int(a[a.index('-m')+1])
if memory>13312:raise SystemExit('guest RAM would exceed 13 GiB')
PY
 mkdir -p "$d"
 if ! test -f "$d/system.raw";then
  saved=$d/failed.qcow2
  test ! -f "$d/after-E7.qcow2" || saved=$d/after-E7.qcow2
  if test -f "$saved";then
   qemu-img convert -f qcow2 -O raw "$saved" "$d/system.raw"
   qemu-img compare -f raw -F qcow2 "$d/system.raw" "$saved"
  else qemu-img create -f raw "$d/system.raw" 32G;fi
 fi
 test -f "$d/vars.fd" || cp /usr/share/OVMF/OVMF_VARS_4M.fd "$d/vars.fd"
 if test -f "$d/network.env";then source "$d/network.env";fi
 printf 'HA_MANAGEMENT_MAC=%q\nHA_PEER_MAC=%q\n' "${HA_MANAGEMENT_MAC:-52:54:00:79:01:0$id}" "${HA_PEER_MAC:-52:54:00:79:00:0$id}" > "$d/network.env"
 seed=${3:-}; if [[ -z $seed && -f $d/seed.iso ]]; then seed=$d/seed.iso; fi
 media=(); test -z "$seed" || media=(-drive "file=$seed,format=raw,if=virtio,snapshot=on")
 boot=();test -z "${HA_BOOT_ISO:-}" || boot=(-cdrom "$HA_BOOT_ISO")
 qemu-system-x86_64 -name "field-edge-ha-$id" -enable-kvm -cpu host -smp 2 -m 4096 -machine q35 \
  -drive if=pflash,format=raw,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.fd \
  -drive "if=pflash,format=raw,file=$d/vars.fd" "${media[@]}" "${boot[@]}" \
  -drive "file=$d/system.raw,format=raw,if=virtio,cache=none" \
  -netdev "user,id=management,hostfwd=tcp:127.0.0.1:25${id}00-:50000,hostfwd=tcp:127.0.0.1:26${id}43-:6443,hostfwd=tcp:127.0.0.1:22${id}22-:22,hostfwd=tcp:127.0.0.1:2808${id}-:30080,hostfwd=tcp:127.0.0.1:2809${id}-:30081" \
  -device "virtio-net-pci,netdev=management,mac=${HA_MANAGEMENT_MAC:-52:54:00:79:01:0$id}" \
  -netdev socket,id=peers,mcast=230.79.0.1:17901,localaddr=127.0.0.1 \
  -device "virtio-net-pci,id=peer-nic,netdev=peers,mac=${HA_PEER_MAC:-52:54:00:79:00:0$id}" \
  -qmp "unix:$d/qmp.sock,server=on,wait=off" -serial "file:$d/serial.log" -display none -daemonize -pidfile "$d/pid"
 ;;
stop) python3 "$FIELD_SCRIPTS/qmp.py" "$d/qmp.sock" quit ;;
link)
 [[ ${3:?up|down} == up ]] && state=true || state=false
 python3 "$FIELD_SCRIPTS/qmp.py" "$d/qmp.sock" set_link "{\"name\":\"peer-nic\",\"up\":$state}"
 ;;
*) exit 2;;
esac
