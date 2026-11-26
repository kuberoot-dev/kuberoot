#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
python3 "$FIELD_SCRIPTS/E2-talos.py" --golden "$GOLDEN" --output "$CASE_OUTPUT" --case "$CASE" --image "$TRIAL_IMAGE" --repetition "$REPETITION"
