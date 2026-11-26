#!/bin/bash
# Boot the original installed baseline disk, add cached W, stop it as a golden.
# E-B1/B2/B3 must have completed before this preparation changes the baseline disk.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
: "${VM_DIR:?}" "${PYTHON_IMAGE:?}" "${KUBECONFIG:?}"
vm_start 1
for i in $(seq 1 120); do
 if kubectl --request-timeout=5s get --raw /readyz; then break; fi
 sleep 5
done
bash "$FIELD_SCRIPTS/deploy-workload.sh"
bash "$FIELD_SCRIPTS/prepare-observer.sh"
# No append request is made here: each clone begins with an empty durable log.
"$FIELD_SCRIPTS/gateway-command.sh" 'python3 -c '\''import urllib.request; assert urllib.request.urlopen("http://10.78.0.11:30080/http").read()==b"W"*1024; assert urllib.request.urlopen("http://10.78.0.11:30081/records").read()==b""; print("cached W ready; durable log empty")'\'''
kubectl -n field get pods -o json > "$VM_DIR/workload-inventory.json"
bash "$FIELD_SCRIPTS/stop-dut.sh" "$VM_DIR"
