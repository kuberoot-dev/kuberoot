#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
for product in release main k3s talos; do
 source "$FIELD_SCRIPTS/select-product.sh" "$product"
 # Original installed disk remains the cold-boot golden; density uses a fresh clone.
 golden=$VM_DIR
 export VM_DIR=$FIELD_ROOT/density-$product
 mkdir "$VM_DIR"
 cp --sparse=always "$golden/system.raw" "$VM_DIR/system.raw"
 cp "$golden/vars.fd" "$VM_DIR/vars.fd"
 vm_start 1
 for i in $(seq 1 120); do
  if kubectl --request-timeout=5s get --raw /readyz; then break; fi
  sleep 5
 done
 kubectl wait --for=condition=Ready nodes --all --timeout=300s
 kubectl create namespace field --dry-run=client -o yaml | kubectl apply -f -
 kubectl -n field create configmap workload --from-file=workload.py="$FIELD_SCRIPTS/workload.py" --dry-run=client -o yaml | kubectl apply -f -
 name=t-E-B3-$PRODUCT
 bash "$FIELD_SCRIPTS/capture.sh" "$name" "$FIELD_SCRIPTS/E-B3-scene.sh"
 python3 "$FIELD_SCRIPTS/verify-capture.py" "$FIELD_ROOT/captures/$name.cast" "$FIELD_ROOT/captures/$name.gif" > "$FIELD_ROOT/captures/$name-verification.json"
 python3 "$FIELD_SCRIPTS/publish-density.py"
 bash "$FIELD_SCRIPTS/stop-dut.sh" "$VM_DIR"
done
