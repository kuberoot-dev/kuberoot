#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
show export "KUBECONFIG=$KUBECONFIG"
python3 "$FIELD_SCRIPTS/E9-kuberoot.py" --golden "$GOLDEN" --output "$CASE_OUTPUT" --cut-percent "$CUT_PERCENT"
