#!/bin/bash
# Small, real scratch I/O in the owned PVC. This validates the harness, not E4.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
D=$(mktemp -d "$FIELD_ROOT/block-fixture.XXXXXX")
qemu-img create -f raw "$D/scratch.raw" 8M
qemu-storage-daemon \
 --chardev "socket,id=qmp,path=$D/qmp.sock,server=on,wait=off" --monitor chardev=qmp \
 --blockdev "driver=file,filename=$D/scratch.raw,node-name=physical,cache.direct=on" \
 --blockdev driver=raw,file=physical,node-name=disk \
 --object throttle-group,id=field-throttle \
 --blockdev driver=throttle,file=disk,throttle-group=field-throttle,node-name=throttled \
 --nbd-server "addr.type=unix,addr.path=$D/nbd.sock" \
 --export type=nbd,id=export,node-name=throttled,writable=on,name=scratch &
pid=$!
trap 'kill "$pid" 2>/dev/null || true' EXIT
for i in $(seq 1 30); do test ! -S "$D/nbd.sock" || break; sleep .1; done
uri="nbd+unix:///scratch?socket=$D/nbd.sock"
qemu-io -f raw -c 'write -P 17 0 4096' "$uri"
python3 "$FIELD_SCRIPTS/block-fault.py" "$D/qmp.sock" 100-iops
python3 "$FIELD_SCRIPTS/block-fault.py" "$D/qmp.sock" unthrottle
python3 "$FIELD_SCRIPTS/block-fault.py" "$D/qmp.sock" eio-on
if qemu-io -f raw -c 'write -P 34 4096 4096' "$uri"; then
 printf 'FAIL: EIO injector did not reject a real write\n' >&2; exit 1
fi
python3 "$FIELD_SCRIPTS/block-fault.py" "$D/qmp.sock" eio-off
qemu-io -f raw -c 'write -P 51 8192 4096' -c 'read -P 17 0 4096' -c 'read -P 51 8192 4096' "$uri"
printf 'PASS: real write failed under EIO, writes/readback recovered after relief; fixture only\n'
