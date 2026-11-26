#!/bin/bash
# Preserve the v1 records; repeat only the three prematurely aborted DNS probes.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
source "$FIELD_SCRIPTS/select-product.sh" release
export BOOT_OUTPUT=$FIELD_ROOT/results/E-B2-dns-wait-v2-$PRODUCT
for REPETITION in 1 2 3; do
 export REPETITION
 name=t-E-B2-$PRODUCT-$REPETITION-dns-wait-v2
 if test -f "$FIELD_ROOT/captures/$name-published"; then continue; fi
 bash "$FIELD_SCRIPTS/capture.sh" "$name" "$FIELD_SCRIPTS/E-B2-scene.sh"
 python3 "$FIELD_SCRIPTS/verify-capture.py" "$FIELD_ROOT/captures/$name.cast" "$FIELD_ROOT/captures/$name.gif" > "$FIELD_ROOT/captures/$name-verification.json"
 python3 "$FIELD_SCRIPTS/publish-boot.py" "$PRODUCT" "$REPETITION" "$name"
 touch "$FIELD_ROOT/captures/$name-published"
done
