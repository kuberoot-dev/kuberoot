#!/bin/bash
# Invoke after all E2 lanes. Each repetition publishes before the next starts.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
for lane in release-testkey main k3s talos; do
 source "$FIELD_SCRIPTS/select-product.sh" "$lane"
 export GOLDEN=$VM_DIR
 unset PHASE_HOOK GOOD_BUNDLE_URL GOOD_BUNDLE_SHA256
 # Published v0.1.0 has no signed bundle asset. Its upgrade lane is the
 # explicitly named lab-trust repack, with unchanged release binaries.
 if [[ $lane == main ]]; then
  export PHASE_HOOK=$FIELD_SCRIPTS/upgrade-power-phase.py
  source "$FIELD_SCRIPTS/select-good-bundle.sh" main
 elif [[ $lane == release-testkey ]]; then
  source "$FIELD_SCRIPTS/select-good-bundle.sh" release-testkey
  export PHASE_HOOK=$FIELD_SCRIPTS/upgrade-power-phase.py
 elif [[ $lane == talos || $lane == k3s ]]; then
  # Product-specific phase hooks must be validated before enabling them.
  # Writes-only is an explicit partial lane; it cannot complete E3 by itself.
  test -z "${COMPETITOR_PHASE_HOOK:-}" || export PHASE_HOOK=$COMPETITOR_PHASE_HOOK
 fi
 for REPETITION in $(seq 1 30); do
  export REPETITION
  index=$(printf '%02d' "$REPETITION")
  directory=$FIELD_ROOT/results/E3-$PRODUCT/E3-$PRODUCT-$index
  test ! -f "$directory/published" || continue
  if test -e "$directory"; then printf 'Review interrupted power trial: %s\n' "$directory" >&2;exit 2;fi
  name=t-E3-$PRODUCT-$index
  bash "$FIELD_SCRIPTS/wan-impairment.sh" clean
  bash "$FIELD_SCRIPTS/capture.sh" "$name" "$FIELD_SCRIPTS/E3-scene.sh"
  python3 "$FIELD_SCRIPTS/chart-power-cuts.py" "$directory"
  python3 "$FIELD_SCRIPTS/publish-case.py" "$directory" "$name"
  touch "$directory/published"
 done
done
