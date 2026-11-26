#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
export KUBECONFIG=$FIELD_ROOT/kuberoot-release/kubeconfig
# Do not mix disk compaction or image downloads into settled idle.
while pgrep -x qemu-img >/dev/null; do sleep 10; done
kubectl -n kube-system rollout status deployment/coredns --timeout=300s
kubectl -n metrics-server rollout status deployment/metrics-server --timeout=300s
kubectl -n local-path-storage rollout status deployment/local-path-provisioner --timeout=300s
sleep 180
bash "$FIELD_SCRIPTS/capture.sh" t-E-B1-kuberoot-release "$FIELD_SCRIPTS/E-B1-kuberoot-release.sh"
python3 - "$FIELD_ROOT" <<'PY'
import json, pathlib, sys
root=pathlib.Path(sys.argv[1]); r=json.loads((root/'results/E-B1-kuberoot-release/result.json').read_text())
r['artifact_paths']=['02-edge/img/t-E-B1-kuberoot-release.gif','02-edge/img/t-E-B1-kuberoot-release.png','02-edge/raw/t-E-B1-kuberoot-release.txt','02-edge/raw/E-B1-kuberoot-release-samples.jsonl']
with (root/'results.jsonl').open('a') as f: f.write(json.dumps(r)+'\n')
PY
