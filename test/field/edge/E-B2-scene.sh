#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
: "${KUBECONFIG:?}" "${PRODUCT:?}" "${IMAGE_DIGEST:?}" "${SOURCE_COMMIT:?}" "${VM_DIR:?}" "${PYTHON_IMAGE:?}"
show export "KUBECONFIG=$KUBECONFIG"
show date -u
show python3 "$FIELD_SCRIPTS/cold-boot-trials.py" --golden "$VM_DIR" --output "${BOOT_OUTPUT:-$FIELD_ROOT/results/E-B2-$PRODUCT}" --repetition "${REPETITION:?}" --product "$PRODUCT" --image "$PYTHON_IMAGE" --image-digest "$IMAGE_DIGEST" --source-commit "$SOURCE_COMMIT"
show date -u
