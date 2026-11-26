#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
show python3 "$FIELD_SCRIPTS/E7.py" --ha-root "$HA_ROOT" --product "$PRODUCT" --replace-hook "$REPLACE_HOOK"
