#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
: "${KUBECONFIG:?}" "${PRODUCT:?}" "${IMAGE_DIGEST:?}" "${SOURCE_COMMIT:?}" "${GOLDEN:?}" "${REPETITION:?}"
args=()
test -z "${PHASE_HOOK:-}" || args+=(--phase-hook "$PHASE_HOOK")
show date -u
show python3 "$FIELD_SCRIPTS/E3.py" --product "$PRODUCT" --image-digest "$IMAGE_DIGEST" --source-commit "$SOURCE_COMMIT" --golden "$GOLDEN" --output "$FIELD_ROOT/results/E3-$PRODUCT" --repetition "$REPETITION" "${args[@]}"
show date -u
