#!/bin/bash
# Continue in table order. No image builds overlap any E1 measurement.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
while ! python3 - "$FIELD_ROOT/results.jsonl" <<'PY'
import json,sys
rows=[json.loads(x) for x in open(sys.argv[1])]
raise SystemExit(0 if len({r['product'] for r in rows if r.get('scenario_id')=='E1' and r.get('measurement_protocol')=='wan-gateway-conntrack-v2' and r.get('status')!='invalid-fixture'})==4 else 1)
PY
do sleep 30; done
while pgrep -f '^bash /work/field-edge/scripts/run-wan-tests.sh$' >/dev/null; do sleep 10; done
date -u
for product in kuberoot-v0.1.0 kuberoot-main-1b29543 k3s-v1.37.2-k3s1 talos-v1.14.7; do
 python3 "$FIELD_SCRIPTS/audit-wan.py" "$FIELD_ROOT/results/E1-gateway-v2-$product"
done
bash "$FIELD_SCRIPTS/prepare-capacity.sh"
bash "$FIELD_SCRIPTS/build-fault-bundles.sh" "$FIELD_ROOT/build-main/out/amd64" main
bash "$FIELD_SCRIPTS/prepare-negative-bundles.sh" main
# This server exposes only public bundle files; credentials are elsewhere.
nohup python3 "$FIELD_SCRIPTS/bundle-server.py" --directory "$FIELD_ROOT/www" --log "$FIELD_ROOT/bundle-requests.jsonl" > "$FIELD_ROOT/bundle-server.log" 2>&1 </dev/null &
echo "$!" > "$FIELD_ROOT/bundle-server.pid"
sleep 1
kill -0 "$(cat "$FIELD_ROOT/bundle-server.pid")"
bash "$FIELD_SCRIPTS/run-upgrade-kuberoot.sh" main
date -u
printf 'E2 main lane ended. Competitor/release lanes and remaining table are still required.\n'
