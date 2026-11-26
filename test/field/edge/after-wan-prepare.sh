#!/bin/bash
# Prepare real E2 artifacts only after all four 60-minute E1 runs have finished.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
while ! python3 - "$FIELD_ROOT/results.jsonl" <<'PY'
import json,sys
rows=[json.loads(x) for x in open(sys.argv[1])]
raise SystemExit(0 if len({r['product'] for r in rows if r.get('scenario_id')=='E1'})==4 else 1)
PY
do sleep 30; done
while pgrep -f '^bash /work/field-edge/scripts/run-wan-tests.sh$' >/dev/null; do sleep 10; done
date -u
bash "$FIELD_SCRIPTS/prepare-capacity.sh"
bash "$FIELD_SCRIPTS/build-fault-bundles.sh" "$FIELD_ROOT/build-main/out/amd64" main
bash "$FIELD_SCRIPTS/build-fault-bundles.sh" /work/series/0.1.0-capture.1 release-testkey
bash "$FIELD_SCRIPTS/build-talos-faults.sh"
date -u
printf 'E2 preparation finished; no product failure scenario is implied by this build.\n'
