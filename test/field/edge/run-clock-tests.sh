#!/bin/bash
# Invoke after E5 is completed; no earlier mandatory row is skipped by this file.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
for product in release main k3s talos; do
 source "$FIELD_SCRIPTS/select-product.sh" "$product"
 export GOLDEN=$VM_DIR
 for CASE in minus-24h plus-400d beyond-ca; do
  export CASE CASE_OUTPUT=$FIELD_ROOT/results/E6-$PRODUCT-$CASE
  name=t-E6-$PRODUCT-$CASE
  bash "$FIELD_SCRIPTS/capture.sh" "$name" "$FIELD_SCRIPTS/E6-scene.sh"
  python3 "$FIELD_SCRIPTS/publish-case.py" "$CASE_OUTPUT" "$name"
 done
done
