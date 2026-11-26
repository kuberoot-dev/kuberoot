#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
export WAN_RUN_LABEL=${WAN_RUN_LABEL:-gateway-v2}
bash "$FIELD_SCRIPTS/wan-impairment.sh" clean
for product in release main k3s talos; do
 source "$FIELD_SCRIPTS/select-product.sh" "$product"
 if ! test -f "$VM_DIR/workload-inventory.json"; then bash "$FIELD_SCRIPTS/prepare-workload-golden.sh"; fi
 golden=$VM_DIR
 export VM_DIR=$FIELD_ROOT/wan-$WAN_RUN_LABEL-$product RUN_NAME=E1-$WAN_RUN_LABEL-$PRODUCT
 mkdir "$VM_DIR"
 cp --sparse=always "$golden/system.raw" "$VM_DIR/system.raw"
 cp "$golden/vars.fd" "$VM_DIR/vars.fd"
 vm_start 1
 for i in $(seq 1 120); do
  if "$FIELD_SCRIPTS/gateway-command.sh" 'curl --fail --max-time 2 http://10.78.0.11:30080/http >/dev/null && curl --fail --max-time 2 http://10.78.0.11:30081/http >/dev/null'; then break; fi
  sleep 5
 done
 name=t-$RUN_NAME
 bash "$FIELD_SCRIPTS/capture.sh" "$name" "$FIELD_SCRIPTS/E1.sh"
 python3 "$FIELD_SCRIPTS/verify-capture.py" "$FIELD_ROOT/captures/$name" > "$FIELD_ROOT/captures/$name-verification.json"
 python3 "$FIELD_SCRIPTS/publish-wan.py"
 bash "$FIELD_SCRIPTS/stop-dut.sh" "$VM_DIR"
 # Preserve the faulted disk losslessly in compressed form before the next DUT.
 qemu-img convert -c -f raw -O qcow2 "$VM_DIR/system.raw" "$VM_DIR/after-fault.qcow2"
 qemu-img compare -f raw -F qcow2 "$VM_DIR/system.raw" "$VM_DIR/after-fault.qcow2"
 rm -- "$VM_DIR/system.raw"
done
