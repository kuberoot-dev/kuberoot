#!/bin/bash
# Exact logical-byte preservation. Golden backing file is immutable, independent
# of the original raw golden that later workload preparation may update.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
failed=${1:?stopped raw disk};golden=${2:?stopped original golden directory}
[[ $failed == /work/field-edge/*/system.raw && $golden == /work/field-edge/* ]] || exit 2
python3 - "$failed" "$golden/system.raw" <<'PY'
import pathlib,sys
for p in pathlib.Path('/proc').glob('[0-9]*/cmdline'):
 try:a=p.read_bytes().decode().split('\0')
 except (OSError,UnicodeError):continue
 if a[0].endswith('qemu-system-x86_64') and any(path in arg for path in sys.argv[1:] for arg in a):raise SystemExit('refusing to snapshot a running VM disk')
PY
base=${3:-$golden/cold-boot-forensic-base.qcow2}
[[ $base == /work/field-edge/*.qcow2 ]] || exit 2
if [[ $# -ge 3 ]]; then test -f "$base"; fi
if ! test -f "$base"; then
 qemu-img convert -c -f raw -O qcow2 "$golden/system.raw" "$base.partial"
 qemu-img compare -f raw -F qcow2 "$golden/system.raw" "$base.partial"
 mv "$base.partial" "$base"
 sha256sum "$base" > "$base.sha256"
fi
out=${failed%/system.raw}/failed.qcow2
test ! -e "$out"
qemu-img convert -c -f raw -O qcow2 -F qcow2 -B "$base" "$failed" "$out"
qemu-img compare -f raw -F qcow2 "$failed" "$out"
rm -- "$failed"
