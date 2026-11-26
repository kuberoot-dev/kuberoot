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
# Keep first case disks and novel/invalid states; repeated known findings
# may be retired only after full public evidence publication.
# Later passing E2 repeats already have complete client/slot/serial evidence.
if python3 - "$failed" <<'PYPASSED'
import datetime,json,pathlib,sys
p=pathlib.Path(sys.argv[1]);result=p.parent/'result.json'
if not p.parent.name.startswith('E2-v2-') or not result.is_file():raise SystemExit(1)
r=json.loads(result.read_text());rep=r.get('repetition',0)
if not (r.get('scenario_id')=='E2' and r.get('measurement_protocol')=='upgrade-client-disconnect-v2' and r.get('status')=='passed' and rep>1 and r.get('acknowledged_record_loss')==0 and p.parent.name.endswith('-'+str(rep))):raise SystemExit(1)
manifest={'source':str(p),'host_allocated_bytes':p.stat().st_blocks*512,'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'reason':'later passing E2 repeat; all public client/slot/serial evidence retained; first case disk and every failed disk retained','result_time_utc':r['time_utc']}
(p.parent/'successful-clone-retirement.json').write_text(json.dumps(manifest,indent=2))
p.unlink()
print(json.dumps(manifest),flush=True)
PYPASSED
then exit 0;fi
if python3 "$FIELD_SCRIPTS/retire-known-e2-repeat.py" "$failed";then exit 0;fi
base=${3:-$golden/cold-boot-forensic-base.qcow2}
if [[ $# -lt 3 ]];then base=$(python3 "$FIELD_SCRIPTS/choose-e2-backing.py" "$failed" "$base");fi
[[ $base == /work/field-edge/*.qcow2 ]] || exit 2
if [[ $# -ge 3 ]]; then test -f "$base"; fi
if ! test -f "$base"; then
 qemu-img convert -c -f raw -O qcow2 "$golden/system.raw" "$base.partial"
 qemu-img compare -f raw -F qcow2 "$golden/system.raw" "$base.partial"
 mv "$base.partial" "$base"
 sha256sum "$base" > "$base.sha256"
fi
out=${PRESERVED_DISK_OUTPUT:-${failed%/system.raw}/failed.qcow2}
[[ $out == /work/field-edge/*.qcow2 ]] || exit 2
test ! -e "$out"
python3 "$FIELD_SCRIPTS/share-disk-blocks.py" "$failed" "$base" "$out" --source-format raw | tee "$out.preservation.jsonl"
qemu-img compare -f raw -F qcow2 "$failed" "$out"
rm -- "$failed"
