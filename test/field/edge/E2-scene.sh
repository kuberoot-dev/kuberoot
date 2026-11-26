#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
: "${PRODUCT:?}" "${GOLDEN:?}" "${CASE:?}" "${REPETITION:?}" "${BUNDLE_URL:?}" "${BUNDLE_SHA256:?}" "${TARGET_VERSION:?}" "${CASE_OUTPUT:?}"
show export "KUBECONFIG=$KUBECONFIG"
show date -u
show python3 "$FIELD_SCRIPTS/upgrade-fault-trial.py" --golden "$GOLDEN" --output "$CASE_OUTPUT" --case "$CASE" --product "$PRODUCT" --repetition "$REPETITION" --bundle-url "$BUNDLE_URL" --bundle-sha256 "$BUNDLE_SHA256" --target-version "$TARGET_VERSION"
show date -u
