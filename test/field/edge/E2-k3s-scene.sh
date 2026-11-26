#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
python3 "$FIELD_SCRIPTS/E2-k3s.py" --case "$CASE" --output "$CASE_OUTPUT" --repetition "$REPETITION"
