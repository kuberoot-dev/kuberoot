#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
show export "KUBECONFIG=$KUBECONFIG"
show python3 "$FIELD_SCRIPTS/E8-talos.py"
