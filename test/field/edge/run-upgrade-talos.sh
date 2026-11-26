#!/bin/bash
# Run only after real installer build/import/signature self-verification succeeds.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
source "$FIELD_SCRIPTS/select-product.sh" talos
export GOLDEN=$VM_DIR
endpoint=$(cat "$FIELD_ROOT/oci-registry/endpoint")
for REPETITION in $(seq 1 10); do
 for CASE in init-crash pod-network wrong-content incompatible-state tampered untrusted; do
  export REPETITION CASE CASE_OUTPUT=$FIELD_ROOT/results/E2-$PRODUCT-$CASE-$REPETITION
  export TRIAL_IMAGE=$endpoint/field-talos/$CASE:trial
  [[ $CASE != tampered ]] || export TRIAL_IMAGE=${endpoint%:*}:15006/field-talos/tampered:trial
  name=t-E2-$PRODUCT-$CASE-$REPETITION
  test ! -f "$CASE_OUTPUT/published" || continue
  if test -e "$CASE_OUTPUT"; then printf 'Review interrupted trial: %s\n' "$CASE_OUTPUT" >&2;exit 2;fi
  bash "$FIELD_SCRIPTS/wan-impairment.sh" clean
  bash "$FIELD_SCRIPTS/capture.sh" "$name" "$FIELD_SCRIPTS/E2-talos-scene.sh"
  python3 "$FIELD_SCRIPTS/publish-case.py" "$CASE_OUTPUT" "$name"
  backing=();first=$FIELD_ROOT/results/E2-$PRODUCT-$CASE-1/failed.qcow2
  [[ $REPETITION == 1 ]] || backing=("$first")
  bash "$FIELD_SCRIPTS/preserve-failed-disk.sh" "$CASE_OUTPUT/system.raw" "$GOLDEN" "${backing[@]}"
  python3 - "$CASE_OUTPUT/result.json" <<'PY'
import json,sys
r=json.load(open(sys.argv[1]))
if r['status']=='invalid-fixture':raise SystemExit('Repair injected fault before repeats: '+r['reason'])
PY
  touch "$CASE_OUTPUT/published"
 done
done
