#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
: "${PRODUCT:?}" "${KUBECONFIG:?}"
# Wait until every non-completed default pod is actually Ready.
ready=false
for i in $(seq 1 120); do
 if kubectl get pods -A -o json | python3 -c 'import json,sys; p=[x for x in json.load(sys.stdin)["items"] if x.get("status",{}).get("phase") not in ["Succeeded"]]; sys.exit(0 if p and all(any(c["type"]=="Ready" and c["status"]=="True" for c in x.get("status",{}).get("conditions",[])) for x in p) else 1)'; then ready=true; break; fi
 sleep 5
done
$ready || { echo "Default pods did not settle; baseline not started" >&2; exit 1; }
sleep 180
bash "$FIELD_SCRIPTS/capture.sh" "t-E-B1-$PRODUCT" "$FIELD_SCRIPTS/E-B1-scene.sh"
python3 - "$FIELD_ROOT" "$PRODUCT" <<'PY'
import json,pathlib,sys
root=pathlib.Path(sys.argv[1]);product=sys.argv[2];r=json.loads((root/f'results/E-B1-{product}/result.json').read_text())
r['artifact_paths']=[f'02-edge/img/t-E-B1-{product}.gif',f'02-edge/img/t-E-B1-{product}.png',f'02-edge/raw/t-E-B1-{product}.txt',f'02-edge/raw/E-B1-{product}-samples.jsonl']
with (root/'results.jsonl').open('a') as f:f.write(json.dumps(r)+'\n')
PY
python3 "$FIELD_SCRIPTS/chart-baseline.py" "$FIELD_ROOT/results/E-B1-$PRODUCT/samples.jsonl" "$FIELD_ROOT/captures/chart-idle-$PRODUCT.png" "$PRODUCT idle memory"
