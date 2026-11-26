#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
args=();test -z "${ETCD_LEADER:-}" || args+=(--leader "$ETCD_LEADER")
show python3 "$FIELD_SCRIPTS/E5.py" --ha-root "$HA_ROOT" --product "$PRODUCT" "${args[@]}"
