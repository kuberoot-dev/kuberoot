#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
source "$FIELD_SCRIPTS/select-product.sh" main
directory=$FIELD_ROOT/results/E10-$PRODUCT
if ! test -f "$directory/published";then
 if test -e "$directory";then printf 'Review interrupted fleet trial: %s\n' "$directory" >&2;exit 2;fi
 if ! test -f "$FIELD_ROOT/fleet-field/prepared";then bash "$FIELD_SCRIPTS/prepare-fleet-field.sh" > "$FIELD_ROOT/fleet-preparation.log" 2>&1;fi
 name=t-E10-$PRODUCT
 bash "$FIELD_SCRIPTS/capture.sh" "$name" "$FIELD_SCRIPTS/E10-scene.sh"
 python3 "$FIELD_SCRIPTS/publish-case.py" "$directory" "$name"
 touch "$directory/published"
fi
# Omni requires a separate service/account not provided to this stand.
python3 - <<'PY'
import datetime,json,pathlib
r={'scenario_id':'E10','product':'Talos + Omni','status':'not measured','reason':'Omni service/account unavailable in this stand, as specified in the binding plan; no simulated result substituted.','source_commit':None,'image_digest':None,'vcpu':None,'ram_mib':None,'topology':None,'fault_seed':None,'fault_phase':None,'outage_seconds':None,'acknowledged_record_loss':None,'recovery_seconds':None,'manual_actions':[],'artifact_paths':[],'time_utc':datetime.datetime.now(datetime.timezone.utc).isoformat()}
p=pathlib.Path('/work/field-edge/results/E10-omni-not-measured');p.mkdir(exist_ok=True)
if (p/'result.json').exists():r=json.loads((p/'result.json').read_text())
else:(p/'result.json').write_text(json.dumps(r,indent=2))
db=pathlib.Path('/work/field-edge/results.jsonl')
existing=[json.loads(line) for line in db.read_text().splitlines()]
if not any(x.get('scenario_id')=='E10' and x.get('product')=='Talos + Omni' and x.get('time_utc')==r['time_utc'] for x in existing):
 with db.open('a') as f:f.write(json.dumps(r)+'\n')
PY
