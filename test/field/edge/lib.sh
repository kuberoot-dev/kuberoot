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
  python3 - "$@" <<'PY'
import sys,time,shlex
print('$ ',end='',flush=True)
for ch in shlex.join(sys.argv[1:]):
    print(ch,end='',flush=True); time.sleep(.012)
print()
PY
  "$@"
}
# All L2 forwarding uses unprivileged multicast. Gateway guests own NAT/netem.
network_create() { require_stand; }
vm_start() {
  local id=$1 media=${2:-${SEED_MEDIA:-}} d="${VM_DIR:-$FIELD_ROOT/vm-$1}"
  [[ $id =~ ^[123]$ ]] || return 1
  require_stand
  [[ $d == "$FIELD_ROOT/"* ]] || return 1
  python3 - "$id" <<'CHECK'
import pathlib,sys
memory=4096
for path in pathlib.Path('/proc').glob('[0-9]*/cmdline'):
    try: args=path.read_bytes().decode().split('\0')
    except (OSError,UnicodeError): continue
    if not args or not args[0].endswith('qemu-system-x86_64'): continue
    if '-name' in args and args[args.index('-name')+1]=='field-edge-'+sys.argv[1]:
        raise SystemExit('DUT with this ID is still running; stop it explicitly first')
    if '-m' in args: memory+=int(args[args.index('-m')+1])
if memory>13312: raise SystemExit('Guest RAM would exceed 13 GiB; reduce topology before launching')
CHECK
  mkdir -p "$d"
  test -f "$d/system.raw" || qemu-img create -f raw "$d/system.raw" 32G
  test -f "$d/vars.fd" || cp /usr/share/OVMF/OVMF_VARS_4M.fd "$d/vars.fd"
  local drives=()
  [[ ${FAULT_FIXTURE:-0} != 1 ]] || drives+=(-smbios type=1,serial=field-edge-fault-test)
  test -z "${BOOT_ISO:-}" || drives+=(-cdrom "$BOOT_ISO")
  test -z "$media" || drives+=(-drive "file=$media,format=raw,if=virtio,snapshot=on")
  local systemdrive=(-drive "file=$d/system.raw,format=raw,if=virtio,cache=none")
  if [[ ${BLOCK_FAULTS:-0} == 1 ]]; then
    systemdrive=(-blockdev "driver=file,filename=$d/system.raw,node-name=physical,cache.direct=on,cache.no-flush=off"
      -blockdev driver=raw,file=physical,node-name=disk
      -object throttle-group,id=field-throttle
      -blockdev driver=throttle,file=disk,throttle-group=field-throttle,node-name=throttled
      -device virtio-blk-pci,drive=throttled,id=system,werror=report,rerror=report)
  fi
  local launch=(qemu-system-x86_64)
  if [[ ${FIELD_IO_TRACE:-0} == 1 ]];then
    test -f "$FIELD_ROOT/io-observation/qemu-io-observation.so"
    launch=(env "LD_PRELOAD=$FIELD_ROOT/io-observation/qemu-io-observation.so" "FIELD_IO_DIAGNOSTIC_LOG=$d/host-io-syscalls.jsonl" qemu-system-x86_64)
  fi
  "${launch[@]}" -name "field-edge-$id" -enable-kvm -cpu host -smp 2 -m 4096 -machine q35 -rtc "base=${RTC_BASE:-utc}" \
    -drive if=pflash,format=raw,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.fd \
    -drive "if=pflash,format=raw,file=$d/vars.fd" "${drives[@]}" \
    "${systemdrive[@]}" \
    -netdev "socket,id=lan,mcast=230.78.0.1:17801,localaddr=127.0.0.1" \
    -device "virtio-net-pci,netdev=lan,mac=52:54:00:78:00:0$id" \
    -qmp "unix:$d/qmp.sock,server=on,wait=off" \
    -serial "file:$d/serial.log" -display none -daemonize -pidfile "$d/pid"
  date -u +%FT%TZ > "$d/boot-start.txt"
}
