#!/bin/bash
set -euo pipefail
source /work/field-edge/scripts/lib.sh
source /work/field-edge/scripts/select-product.sh main
export KUBECONFIG=$NODE_KUBECONFIG
show export "KUBECONFIG=$KUBECONFIG"
show date -u
show kubectl get upgrade field-bad-release -o yaml
show kubectl get bootentries
show python3 /work/field-edge/eio-block-counters.py
show df -h /work
show date -u
