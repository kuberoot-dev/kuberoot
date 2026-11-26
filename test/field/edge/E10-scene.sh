#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
python3 "$FIELD_SCRIPTS/E10.py"
