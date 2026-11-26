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
 cases=${UPGRADE_CASES:-"init-crash pod-network wrong-content incompatible-state tampered untrusted"}
 if [[ -n ${UPGRADE_CASES:-} && $lane != health ]];then echo "Case subset is restricted to the supplementary fixed lane" >&2;exit 2;fi
 for CASE in $cases; do
 export CASE TARGET_VERSION=0.1.0-field-$lane-$CASE
 # Negative fixtures use the same wrong-content payload, rejected before boot.
 [[ $CASE != tampered && $CASE != untrusted ]] || export TARGET_VERSION=0.1.0-field-$lane-wrong-content
 export BUNDLE_URL=http://10.0.2.2:15004/0.1.0-field-$lane-$CASE.tar
 export BUNDLE_SHA256=$(cut -d' ' -f1 "$base-$CASE.tar.sha256")
  export REPETITION
  case_base=$FIELD_ROOT/results/E2-v2-$PRODUCT-$CASE-$REPETITION
  while true; do
  export CASE_OUTPUT=$case_base
  if test -f "$CASE_OUTPUT/retry.json"; then
   CASE_OUTPUT=$(python3 "$FIELD_SCRIPTS/reviewed-retry.py" "$CASE_OUTPUT")
   export CASE_OUTPUT
  fi
  name=t-${CASE_OUTPUT##*/}
  if test -f "$CASE_OUTPUT/published"; then break; fi
  if test -e "$CASE_OUTPUT"; then
   printf 'Interrupted case requires evidence review: %s\n' "$CASE_OUTPUT" >&2;exit 2
  fi
  bash "$FIELD_SCRIPTS/wan-impairment.sh" clean
  bash "$FIELD_SCRIPTS/capture.sh" "$name" "$FIELD_SCRIPTS/E2-scene.sh"
  python3 "$FIELD_SCRIPTS/publish-case.py" "$CASE_OUTPUT" "$name"
  # Preserve first/new/invalid states against immutable backing snapshots.
  # Published repeats may be retired under the explicit evidence-retention guard.
  backing=()
  first=$FIELD_ROOT/results/E2-v2-$PRODUCT-$CASE-1/failed.qcow2
  if [[ $REPETITION != 1 ]]; then backing=("$first"); fi
  bash "$FIELD_SCRIPTS/preserve-failed-disk.sh" "$CASE_OUTPUT/system.raw" "$GOLDEN" "${backing[@]}"
  status=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["status"])' "$CASE_OUTPUT/result.json")
  if [[ $status == invalid-fixture ]];then
   python3 "$FIELD_SCRIPTS/review-backend-retry.py" "$CASE_OUTPUT" "$case_base"
   continue
  fi
  touch "$CASE_OUTPUT/published"
  break
  done
 done
done
