#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
source "$FIELD_SCRIPTS/select-product.sh" main
if ! test -f "$FIELD_ROOT/restore-fixture/prepared"; then
 python3 "$FIELD_SCRIPTS/prepare-restore-fixture.py" > "$FIELD_ROOT/restore-preparation.log" 2>&1
fi
for CASE in valid wrong-key truncated s3-down; do
 export CASE CASE_OUTPUT=$FIELD_ROOT/results/E8-$PRODUCT-$CASE
 name=t-E8-$PRODUCT-$CASE
 test ! -f "$CASE_OUTPUT/published" || continue
 if test -e "$CASE_OUTPUT"; then printf 'Review interrupted restore trial: %s\n' "$CASE_OUTPUT" >&2;exit 2;fi
 bash "$FIELD_SCRIPTS/capture.sh" "$name" "$FIELD_SCRIPTS/E8-scene.sh"
 python3 "$FIELD_SCRIPTS/publish-case.py" "$CASE_OUTPUT" "$name"
 touch "$CASE_OUTPUT/published"
 bash "$FIELD_SCRIPTS/preserve-failed-disk.sh" "$CASE_OUTPUT/system.raw" "$VM_DIR"
done
source "$FIELD_SCRIPTS/select-product.sh" talos
name=t-E8-talos-v1.14.7-valid
directory=$FIELD_ROOT/results/E8-talos-v1.14.7-valid
if ! test -f "$directory/published";then
 bash "$FIELD_SCRIPTS/capture.sh" "$name" "$FIELD_SCRIPTS/E8-talos-scene.sh"
 python3 "$FIELD_SCRIPTS/publish-case.py" "$directory" "$name"
 touch "$directory/published"
 bash "$FIELD_SCRIPTS/preserve-failed-disk.sh" "$directory/replacement/system.raw" "$VM_DIR"
fi
