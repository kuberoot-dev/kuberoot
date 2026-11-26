#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
show date -u
show kubectl get nodes -o wide
show python3 "$FIELD_SCRIPTS/E-B3.py" --product "$PRODUCT" --image "$PYTHON_IMAGE" --output "$FIELD_ROOT/results/E-B3-$PRODUCT"
show kubectl -n field get deployment density
show date -u
