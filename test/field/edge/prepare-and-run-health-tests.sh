#!/bin/bash
# After original E2 comparisons, before E3. Never overwrite interrupted work.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
require_stand
python3 "$FIELD_SCRIPTS/audit-suite.py" "$FIELD_ROOT/results.jsonl" --through E2
checkpoint() {
 local label=$1;shift
 local state=$FIELD_ROOT/preparation-checkpoints
 mkdir -p "$state"
 test ! -f "$state/$label.complete" || return 0
 test ! -f "$state/$label.started" || { printf 'Review interrupted preparation: %s\n' "$label" >&2;return 2; }
 date -u > "$state/$label.started"
 "$@" > "$state/$label.log" 2>&1
 date -u > "$state/$label.complete"
}
checkpoint health-image bash "$FIELD_SCRIPTS/build-health-image.sh"
checkpoint health-golden bash "$FIELD_SCRIPTS/prepare-health-golden.sh"
checkpoint health-faults bash "$FIELD_SCRIPTS/build-fault-bundles.sh" "$FIELD_ROOT/build-health/out/amd64" health
bash "$FIELD_SCRIPTS/run-health-upgrade-tests.sh"
python3 - "$FIELD_ROOT/results.jsonl" <<'PYCHECK'
import json,sys
rows=[json.loads(line) for line in open(sys.argv[1])]
for phase in ['healthy-control','wrong-content','incompatible-state']:
 selected=[r for r in rows if r.get('scenario_id')=='E2' and r.get('product')=='kuberoot-health-52db7b1' and r.get('fault_phase')==phase and r.get('status')!='invalid-fixture']
 assert set(range(1,11))<={r.get('repetition') for r in selected},'Fixed lane incomplete: '+phase
 if phase!='incompatible-state':assert all(r['status']=='passed' for r in selected),'Application-health implementation needs diagnosis: '+phase
 # Persistent-state damage is intentionally not repaired by root-slot rollback.
 if phase=='incompatible-state':assert all(r.get('metrics',{}).get('automatic_rollback_seconds') is not None for r in selected),'Application-health rollback not observed for state migration'
PYCHECK
