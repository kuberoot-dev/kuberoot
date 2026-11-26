#!/bin/bash
# Supplementary E2 control and c/d reruns; original main evidence is retained.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
require_stand
: "${FIELD_ROOT:?}"
test -f "$FIELD_ROOT/build-health/built"
test -f "$FIELD_ROOT/vm-health/system.raw"
export HEALTH_CHECKS_FILE=$FIELD_SCRIPTS/application-health-checks.json
export UPGRADE_CASES="healthy-control wrong-content incompatible-state"
bash "$FIELD_SCRIPTS/run-upgrade-kuberoot.sh" health
