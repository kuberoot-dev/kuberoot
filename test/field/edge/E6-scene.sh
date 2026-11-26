#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
show export "KUBECONFIG=${KUBECONFIG:?}"
show date -u
show python3 "$FIELD_SCRIPTS/clock-drift-trials.py" --golden "${GOLDEN:?}" --output "${CASE_OUTPUT:?}" --product "${PRODUCT:?}" --case "${CASE:?}"
show date -u
