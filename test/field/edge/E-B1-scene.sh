#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
: "${KUBECONFIG:?}" "${PRODUCT:?}" "${IMAGE_DIGEST:?}" "${SOURCE_COMMIT:?}" "${VM_DIR:?}"
show date -u
show kubectl get nodes
show kubectl get pods -A
show python3 "$FIELD_SCRIPTS/E-B1.py" --product "$PRODUCT" --image-digest "$IMAGE_DIGEST" \
 --source-commit "$SOURCE_COMMIT" --qmp "$VM_DIR/qmp.sock" --output "$FIELD_ROOT/results/E-B1-$PRODUCT"
show date -u
