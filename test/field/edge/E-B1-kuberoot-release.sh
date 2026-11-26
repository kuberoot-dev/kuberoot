#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
export KUBECONFIG=$FIELD_ROOT/kuberoot-release/kubeconfig
show date -u
show kubectl get nodes
show kubectl get pods -A
show python3 "$FIELD_SCRIPTS/E-B1.py" --product kuberoot-v0.1.0 \
 --image-digest sha256:29b12ff8abc63ebd05a085b6a1f2c9f131faf7d9ae57f92d6a5e1cc250ef01ce \
 --source-commit ffcc71a4c6c7d3aa36c37221f0cd1d19c22e99b0 \
 --qmp "$FIELD_ROOT/vm-1/qmp.sock" --output "$FIELD_ROOT/results/E-B1-kuberoot-release"
show date -u
