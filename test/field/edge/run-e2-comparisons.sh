#!/bin/bash
# Continue E2 after the completed main lane has been reviewed. No earlier row
# is skipped; setup failures stop for diagnosis and do not become unsupported.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
require_stand
python3 - "$FIELD_ROOT/results.jsonl" <<'PY'
import json,sys
rows=[json.loads(line) for line in open(sys.argv[1])]
for phase in ['init-crash','pod-network','wrong-content','incompatible-state','tampered','untrusted']:
 done={r.get('repetition') for r in rows if r.get('scenario_id')=='E2' and r.get('product')=='kuberoot-main-1b29543' and r.get('fault_phase')==phase and not r.get('status','').startswith('invalid')}
 assert set(range(1,11))<=done,'E2 main lane is incomplete: '+phase
PY
# Markers mean a preparation command returned successfully. If interrupted,
# retain its partial directory/log and diagnose it rather than overwriting it.
checkpoint() {
 local label=$1;shift
 local state=$FIELD_ROOT/preparation-checkpoints
 mkdir -p "$state"
 test ! -f "$state/$label.complete" || return 0
 test ! -f "$state/$label.started" || { printf 'Review interrupted preparation: %s\n' "$label" >&2; return 2; }
 date -u > "$state/$label.started"
 "$@" > "$state/$label.log" 2>&1
 date -u > "$state/$label.complete"
}
checkpoint release-testkey-golden bash "$FIELD_SCRIPTS/prepare-release-testkey.sh"
checkpoint release-testkey-faults bash "$FIELD_SCRIPTS/build-fault-bundles.sh" /work/series/0.1.0-capture.1 release-testkey
checkpoint release-testkey-negative bash "$FIELD_SCRIPTS/prepare-negative-bundles.sh" release-testkey
bash "$FIELD_SCRIPTS/run-upgrade-kuberoot.sh" release-testkey
checkpoint k3s-upgrade-golden bash "$FIELD_SCRIPTS/prepare-k3s-upgrade-golden.sh"
checkpoint k3s-fault-packages bash "$FIELD_SCRIPTS/build-k3s-fault-packages.sh"
bash "$FIELD_SCRIPTS/run-upgrade-k3s.sh"
checkpoint talos-faults bash "$FIELD_SCRIPTS/build-talos-faults.sh"
# TLS transport preparation exists from the earlier fixture validation.
# It is never an image-signature authorization policy.
test -s "$FIELD_ROOT/registry-tls/ca.crt"
checkpoint talos-oci-registry bash "$FIELD_SCRIPTS/prepare-oci-registry.sh"
checkpoint talos-negative-images bash "$FIELD_SCRIPTS/prepare-talos-negative-images.sh"
bash "$FIELD_SCRIPTS/run-upgrade-talos.sh"
date -u
