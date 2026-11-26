#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
export PRODUCT=k3s-v1.37.1-to-v1.37.2-lab-package GOLDEN=$FIELD_ROOT/k3s-upgrade-a
for REPETITION in $(seq 1 10); do
 for CASE in init-crash pod-network wrong-content incompatible-state tampered untrusted; do
  export REPETITION CASE CASE_OUTPUT=$FIELD_ROOT/results/E2-$PRODUCT-$CASE-$REPETITION
  name=t-E2-$PRODUCT-$CASE-$REPETITION
  test ! -f "$CASE_OUTPUT/published" || continue
  if test -e "$CASE_OUTPUT"; then printf 'Review interrupted trial: %s\n' "$CASE_OUTPUT" >&2;exit 2;fi
  bash "$FIELD_SCRIPTS/wan-impairment.sh" clean
  bash "$FIELD_SCRIPTS/capture.sh" "$name" "$FIELD_SCRIPTS/E2-k3s-scene.sh"
  python3 "$FIELD_SCRIPTS/publish-case.py" "$CASE_OUTPUT" "$name"
  touch "$CASE_OUTPUT/published"
  backing=();first=$FIELD_ROOT/results/E2-$PRODUCT-$CASE-1/failed.qcow2
  [[ $REPETITION == 1 ]] || backing=("$first")
  bash "$FIELD_SCRIPTS/preserve-failed-disk.sh" "$CASE_OUTPUT/system.raw" "$GOLDEN" "${backing[@]}"
  python3 - "$CASE_OUTPUT/result.json" <<'PY'
import json,sys
r=json.load(open(sys.argv[1]))
if r['status']=='invalid-fixture':raise SystemExit('Repair injected fault before repeats: '+r['reason'])
PY
 done
done
