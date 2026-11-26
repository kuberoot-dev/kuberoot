#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
source "$FIELD_SCRIPTS/select-product.sh" main
if ! test -f "$FIELD_ROOT/fleet-field/prepared";then bash "$FIELD_SCRIPTS/prepare-fleet-field.sh" > "$FIELD_ROOT/fleet-preparation.log" 2>&1;fi
name=t-E10-$PRODUCT
bash "$FIELD_SCRIPTS/capture.sh" "$name" "$FIELD_SCRIPTS/E10-scene.sh"
python3 "$FIELD_SCRIPTS/publish-case.py" "$FIELD_ROOT/results/E10-$PRODUCT" "$name"
# Omni requires a separate service/account not provided to this stand.
python3 - <<'PY'
import datetime,json,pathlib
r={'scenario_id':'E10','product':'Talos + Omni','status':'not measured','reason':'Omni service/account unavailable in this stand, as specified in the binding plan; no simulated result substituted.','source_commit':None,'image_digest':None,'vcpu':None,'ram_mib':None,'topology':None,'fault_seed':None,'fault_phase':None,'outage_seconds':None,'acknowledged_record_loss':None,'recovery_seconds':None,'manual_actions':[],'artifact_paths':[],'time_utc':datetime.datetime.now(datetime.timezone.utc).isoformat()}
p=pathlib.Path('/work/field-edge/results/E10-omni-not-measured');p.mkdir();(p/'result.json').write_text(json.dumps(r,indent=2))
with pathlib.Path('/work/field-edge/results.jsonl').open('a') as f:f.write(json.dumps(r)+'\n')
PY
