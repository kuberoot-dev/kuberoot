#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
for lane in release-testkey main; do
 source "$FIELD_SCRIPTS/select-product.sh" "$lane"
 source "$FIELD_SCRIPTS/select-good-bundle.sh" "$lane"
 export GOLDEN=$VM_DIR
 for CUT_PERCENT in 10 50 90; do
  export CUT_PERCENT CASE_OUTPUT=$FIELD_ROOT/results/E9-$PRODUCT-cut-$CUT_PERCENT
  name=t-E9-$PRODUCT-cut-$CUT_PERCENT
  test ! -f "$CASE_OUTPUT/published" || continue
  if test -e "$CASE_OUTPUT"; then printf 'Review interrupted window trial: %s\n' "$CASE_OUTPUT" >&2;exit 2;fi
  bash "$FIELD_SCRIPTS/capture.sh" "$name" "$FIELD_SCRIPTS/E9-scene.sh"
  python3 "$FIELD_SCRIPTS/chart-transfer.py" "$CASE_OUTPUT"
  python3 "$FIELD_SCRIPTS/publish-case.py" "$CASE_OUTPUT" "$name"
  touch "$CASE_OUTPUT/published"
  bash "$FIELD_SCRIPTS/preserve-failed-disk.sh" "$CASE_OUTPUT/system.raw" "$GOLDEN"
 done
done
printf 'Kuberoot windows ended; Talos installer transfer comparison remains required.\n'
