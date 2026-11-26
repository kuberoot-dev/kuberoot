#!/bin/bash
set -euo pipefail
source /work/field-edge/scripts/lib.sh
source /work/field-edge/scripts/select-product.sh main
export PATH=/work/field-edge/io-diagnosis/bin:$PATH
export FIELD_IO_DIAGNOSTIC_LOG=/work/field-edge/results/E2-diagnostic-main-pod-network-io-1/host-io-syscalls.jsonl
show date -u
show export "KUBECONFIG=$KUBECONFIG"
show python3 /work/field-edge/scripts/upgrade-fault-trial.py --golden "$VM_DIR" --output /work/field-edge/results/E2-diagnostic-main-pod-network-io-1 --case pod-network --product kuberoot-main-1b29543 --repetition 2 --bundle-url http://10.0.2.2:15004/0.1.0-field-main-pod-network.tar --bundle-sha256 "$(cut -d' ' -f1 /work/field-edge/www/0.1.0-field-main-pod-network.tar.sha256)" --target-version 0.1.0-field-main-pod-network
show date -u
