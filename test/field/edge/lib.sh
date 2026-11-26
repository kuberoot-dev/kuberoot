#!/bin/bash
# All runtime paths are in the owned PVC. Never run VM commands on the Mac.
set -euo pipefail
export PATH=/work/bin:$PATH TMPDIR=/work/tmp
FIELD_ROOT=${FIELD_ROOT:-/work/field-edge}
FIELD_SCRIPTS=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
require_stand() {
  test "$(uname -s)" = Linux || { echo 'Run inside kr-edge-stand only' >&2; return 1; }
  test "$FIELD_ROOT" = /work/field-edge || { echo 'Unexpected field root' >&2; return 1; }
  python3 "$FIELD_SCRIPTS/preflight.py"
}
show() {
  python3 - "$*" <<'PY'
import sys,time
print('$ ',end='',flush=True)
for ch in sys.argv[1]:
    print(ch,end='',flush=True); time.sleep(.012)
print()
PY
  "$@"
}
# All L2 forwarding uses unprivileged multicast. Gateway guests own NAT/netem.
network_create() { require_stand; }
vm_start() {
  local id=$1 media=${2:-} d="$FIELD_ROOT/vm-$1"
  [[ $id =~ ^[123]$ ]] || return 1
  require_stand
  mkdir -p "$d"
  test -f "$d/system.raw" || qemu-img create -f raw "$d/system.raw" 32G
  test -f "$d/vars.fd" || cp /usr/share/OVMF/OVMF_VARS_4M.fd "$d/vars.fd"
  local drives=()
  test -z "$media" || drives+=(-drive "file=$media,format=raw,if=virtio,snapshot=on")
  qemu-system-x86_64 -name "field-edge-$id" -enable-kvm -cpu host -smp 2 -m 4096 -machine q35 \
    -drive if=pflash,format=raw,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.fd \
    -drive "if=pflash,format=raw,file=$d/vars.fd" "${drives[@]}" \
    -drive "file=$d/system.raw,format=raw,if=virtio,cache=none" \
    -netdev "socket,id=lan,mcast=230.78.0.1:17801,localaddr=127.0.0.1" \
    -device "virtio-net-pci,netdev=lan,mac=52:54:00:78:00:0$id" \
    -qmp "unix:$d/qmp.sock,server=on,wait=off" \
    -serial "file:$d/serial.log" -display none -daemonize -pidfile "$d/pid"
  date -u +%FT%TZ > "$d/boot-start.txt"
}
