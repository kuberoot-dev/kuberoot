#!/bin/bash
# E3 must finish first. Injector setup is outside the timed storage experiment.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
for lane in release main k3s talos; do
 source "$FIELD_SCRIPTS/select-product.sh" "$lane"
 original=$VM_DIR
 export VM_DIR=$FIELD_ROOT/storage-golden-$lane
 if ! test -f "$VM_DIR/prepared"; then
  mkdir "$VM_DIR"
  cp --sparse=always "$original/system.raw" "$VM_DIR/system.raw"
  cp "$original/vars.fd" "$VM_DIR/vars.fd"
  vm_start 1
  for i in $(seq 1 150); do
   if kubectl --request-timeout=5s get --raw /readyz; then break;fi
   sleep 2
  done
  bash "$FIELD_SCRIPTS/deploy-storage-injector.sh"
  "$FIELD_SCRIPTS/gateway-command.sh" 'curl --fail --max-time 5 -X POST http://10.78.0.11:30082/relieve'
  bash "$FIELD_SCRIPTS/stop-dut.sh" "$VM_DIR"
  touch "$VM_DIR/prepared"
 fi
 export GOLDEN=$VM_DIR
 for CASE in 100-iops 20-iops full-90 full-95 full-100 eio; do
  export CASE CASE_OUTPUT=$FIELD_ROOT/results/E4-$PRODUCT-$CASE
  test ! -f "$CASE_OUTPUT/published" || continue
  if test -e "$CASE_OUTPUT"; then printf 'Review interrupted storage trial: %s\n' "$CASE_OUTPUT" >&2;exit 2;fi
  name=t-E4-$PRODUCT-$CASE
  bash "$FIELD_SCRIPTS/capture.sh" "$name" "$FIELD_SCRIPTS/E4-scene.sh"
  python3 "$FIELD_SCRIPTS/publish-case.py" "$CASE_OUTPUT" "$name"
  touch "$CASE_OUTPUT/published"
  bash "$FIELD_SCRIPTS/preserve-failed-disk.sh" "$CASE_OUTPUT/system.raw" "$GOLDEN"
 done
done
