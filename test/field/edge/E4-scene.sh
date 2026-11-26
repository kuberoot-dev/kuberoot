#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
show export "KUBECONFIG=$KUBECONFIG"
args=();test -z "${STORAGE_UPGRADE_HOOK:-}" || args+=(--upgrade-hook "$STORAGE_UPGRADE_HOOK")
show python3 "$FIELD_SCRIPTS/E4.py" --golden "$GOLDEN" --output "$CASE_OUTPUT" --case "$CASE" --product "$PRODUCT" "${args[@]}"
