#!/bin/bash
# Invoke after all E2 lanes. Each repetition publishes before the next starts.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
for lane in release-testkey main k3s talos; do
 source "$FIELD_SCRIPTS/select-product.sh" "$lane"
 export GOLDEN=$VM_DIR
 unset PHASE_HOOK GOOD_BUNDLE_URL GOOD_BUNDLE_SHA256 POWER_PHASES
 # Published v0.1.0 has no signed bundle asset. Its upgrade lane is the
 # explicitly named lab-trust repack, with unchanged release binaries.
 if [[ $lane == main ]]; then
  export PHASE_HOOK=$FIELD_SCRIPTS/upgrade-power-phase.py
  source "$FIELD_SCRIPTS/select-good-bundle.sh" main
 elif [[ $lane == release-testkey ]]; then
  source "$FIELD_SCRIPTS/select-good-bundle.sh" release-testkey
  export PHASE_HOOK=$FIELD_SCRIPTS/upgrade-power-phase.py
 elif [[ $lane == k3s ]]; then
  export GOLDEN=$FIELD_ROOT/k3s-upgrade-a KUBECONFIG=$FIELD_ROOT/k3s-upgrade-a/kubeconfig SEED_MEDIA=$FIELD_ROOT/k3s-upgrade-a/seed.iso DUT_KNOWN_HOSTS=$FIELD_ROOT/k3s-upgrade-a/known_hosts
  export PHASE_HOOK=$FIELD_SCRIPTS/k3s-power-phase.py POWER_PHASES=writes,download,package-write,installed-before-reboot,early-boot
  export PRODUCT=k3s-v1.37.1-to-v1.37.2-lab-package
  bash "$FIELD_SCRIPTS/prepare-k3s-good-package.sh"
 elif [[ $lane == talos ]]; then
  test -f "$FIELD_ROOT/talos-good/image.json" || bash "$FIELD_SCRIPTS/prepare-talos-good-image.sh"
  export PHASE_HOOK=$FIELD_SCRIPTS/talos-power-phase.py
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
  python3 - "$directory/result.json" <<'PYCASE'
import json,sys
r=json.load(open(sys.argv[1]))
if r['status']=='invalid-fixture':raise SystemExit('Repair power-cut fixture before repeats: '+r['reason'])
PYCASE
 done
done
