#!/bin/bash
# Collect real evidence even when a failed DUT cannot export its durable log.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
: "${RUN_NAME:?}" "${VM_DIR:?}"
D=$FIELD_ROOT/results/$RUN_NAME;G=$FIELD_SCRIPTS/gateway-command.sh
for i in $(seq 1 60);do
 if "$G" "test -f /home/observer/$RUN_NAME.jsonl.done";then break;fi
 sleep 1
done
"$G" "test -f /home/observer/$RUN_NAME.jsonl.done"
"$G" "touch /home/observer/$RUN_NAME-monitor.stop"
sleep 2
"$G" "cat /home/observer/$RUN_NAME-monitor.jsonl" > "$D/gateway-monitor.jsonl"
"$G" "cat /home/observer/$RUN_NAME.jsonl" > "$D/observer.jsonl"
if "$G" 'curl --fail --max-time 20 http://10.78.0.11:30081/records' > "$D/recovered-records.partial" 2> "$D/record-retrieval-error.txt";then
 mv "$D/recovered-records.partial" "$D/recovered-records"
 python3 "$FIELD_SCRIPTS/check-records.py" "$D/observer.jsonl" "$D/recovered-records" > "$D/durability.json" || true
else
 python3 - "$D" <<'PY'
import json,pathlib,sys
p=pathlib.Path(sys.argv[1]);rows=[json.loads(x) for x in (p/'observer.jsonl').read_text().splitlines()]
r={'acknowledged':sum(bool(x.get('acknowledged')) for x in rows),'recovered':None,'acknowledged_record_loss':None,'corrupt_records':None,'duplicate_records':None,'retrieval_error':(p/'record-retrieval-error.txt').read_text(),'scope':'Durability cannot be determined because the real record retrieval failed.'}
(p/'durability.json').write_text(json.dumps(r,indent=2))
PY
fi
show cat "$D/durability.json"
python3 "$FIELD_SCRIPTS/analyse-observer.py" "$D/observer.jsonl" --fault-epoch "$(cat "$D/power-cut-epoch")" | tee "$D/recovery.json"
cp "$VM_DIR/serial.log" "$D/recovery-serial.log"
python3 "$FIELD_SCRIPTS/render-serial.py" "$D/recovery.cast"
