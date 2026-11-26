#!/bin/bash
# Run only after the four E-B1 records have been published.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
for product in release main k3s talos; do
 source "$FIELD_SCRIPTS/select-product.sh" "$product"
 bash "$FIELD_SCRIPTS/prepare-boot-baseline.sh"
 for REPETITION in $(seq 1 10); do
  export REPETITION
  name=t-E-B2-$PRODUCT-$REPETITION
  # Measured failures are retained; an infrastructure error stops for diagnosis.
  bash "$FIELD_SCRIPTS/capture.sh" "$name" "$FIELD_SCRIPTS/E-B2-scene.sh"
  python3 "$FIELD_SCRIPTS/verify-capture.py" "$FIELD_ROOT/captures/$name.cast" "$FIELD_ROOT/captures/$name.gif" > "$FIELD_ROOT/captures/$name-verification.json"
  python3 "$FIELD_SCRIPTS/publish-boot.py" "$PRODUCT" "$REPETITION" "$name"
 done
done
