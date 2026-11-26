#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
python3 "$FIELD_SCRIPTS/E8-kuberoot.py" --case "$CASE" --output "$CASE_OUTPUT"
