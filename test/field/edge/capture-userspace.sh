#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
export KUBECONFIG=$FIELD_ROOT/kuberoot-release/kubeconfig
show date -u
show "$FIELD_SCRIPTS/gateway-command.sh" 'ip -br address; sudo nft list table ip nat'
show kubectl get nodes -o wide
show kubectl get pods -A
show date -u
