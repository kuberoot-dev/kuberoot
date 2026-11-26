#!/bin/bash
# Add only the pinned image cache, then cleanly power off the golden installed disk.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
: "${KUBECONFIG:?}" "${VM_DIR:?}" "${PYTHON_IMAGE:?}"
vm_start 1
for i in $(seq 1 120); do
 if kubectl --request-timeout=5s get --raw /readyz; then break; fi
 sleep 5
done
kubectl -n default run field-image-cache --image="$PYTHON_IMAGE" --restart=Never --command -- python3 -c 'print("image cached")'
kubectl -n default wait --for=jsonpath='{.status.phase}'=Succeeded pod/field-image-cache --timeout=300s
kubectl -n default get pod field-image-cache -o json > "$VM_DIR/workload-image-cache.json"
kubectl -n default delete pod field-image-cache --wait=true
bash "$FIELD_SCRIPTS/stop-dut.sh" "$VM_DIR"
