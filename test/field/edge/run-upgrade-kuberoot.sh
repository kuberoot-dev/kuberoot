#!/bin/bash
# One full lane. Invoke only after E1 comparison has finished, with no DUT active.
# Completed cases are immutable; interrupted directories require explicit review.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
lane=${1:?main or release-testkey}
source "$FIELD_SCRIPTS/select-product.sh" "$lane"
export GOLDEN=$VM_DIR
base=$FIELD_ROOT/www/0.1.0-field-$lane
for REPETITION in $(seq 1 10); do
 for CASE in init-crash pod-network wrong-content incompatible-state tampered untrusted; do
 export CASE TARGET_VERSION=0.1.0-field-$lane-$CASE
 # Negative fixtures use the same wrong-content payload, rejected before boot.
 [[ $CASE != tampered && $CASE != untrusted ]] || export TARGET_VERSION=0.1.0-field-$lane-wrong-content
 export BUNDLE_URL=http://10.0.2.2:15004/0.1.0-field-$lane-$CASE.tar
 export BUNDLE_SHA256=$(cut -d' ' -f1 "$base-$CASE.tar.sha256")
  export REPETITION CASE_OUTPUT=$FIELD_ROOT/results/E2-$PRODUCT-$CASE-$REPETITION
  name=t-E2-$PRODUCT-$CASE-$REPETITION
  if test -f "$CASE_OUTPUT/published"; then continue; fi
  if test -e "$CASE_OUTPUT"; then
   printf 'Interrupted case requires evidence review: %s\n' "$CASE_OUTPUT" >&2;exit 2
  fi
  bash "$FIELD_SCRIPTS/wan-impairment.sh" clean
  bash "$FIELD_SCRIPTS/capture.sh" "$name" "$FIELD_SCRIPTS/E2-scene.sh"
  python3 "$FIELD_SCRIPTS/publish-case.py" "$CASE_OUTPUT" "$name"
  touch "$CASE_OUTPUT/published"
  # Preserve every failed state using an independently pinned backing snapshot.
  # Successful clones are also retained until reviewed; remove no measured state.
  bash "$FIELD_SCRIPTS/preserve-failed-disk.sh" "$CASE_OUTPUT/system.raw" "$GOLDEN"
  python3 - "$CASE_OUTPUT/result.json" <<'PYCASE'
import json,sys
r=json.load(open(sys.argv[1]))
if r["status"]=="invalid-fixture":raise SystemExit("Fault fixture invalid; repair before further repetitions: "+r["reason"])
PYCASE
 done
done
