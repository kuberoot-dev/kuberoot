#!/bin/bash
# A resumable stage queue, not a declaration that the whole field suite passed.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
while ! python3 - "$FIELD_ROOT/results.jsonl" <<'PY'
import json,pathlib,sys
p=pathlib.Path(sys.argv[1]);r=[json.loads(x) for x in p.read_text().splitlines()] if p.exists() else []
products={x['product'] for x in r if x.get('scenario_id')=='E-B1' and x.get('status')=='measured'}
raise SystemExit(0 if len(products)==4 else 1)
PY
do sleep 15; done
# The final baseline publishes before rendering its extra chart. Let it finish.
sleep 30
bash "$FIELD_SCRIPTS/stop-dut.sh" "$FIELD_ROOT/talos"
date -u
bash "$FIELD_SCRIPTS/run-cold-boots.sh"
date -u
bash "$FIELD_SCRIPTS/run-density.sh"
date -u
bash "$FIELD_SCRIPTS/run-wan-tests.sh"
date -u
printf 'E-B2, E-B3 and E1 stage queue finished. E2 through E10 remain mandatory.\n'
