#!/bin/bash
# Start after E2 main is complete and reviewed. The foreground log persists
# across disconnects when this wrapper is launched with nohup inside the pod.
# Product failures are findings; invalid fixtures require repair and a rerun.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
require_stand
bash "$FIELD_SCRIPTS/run-e2-comparisons.sh"
python3 "$FIELD_SCRIPTS/audit-suite.py" "$FIELD_ROOT/results.jsonl" --through E2
for pair in E3:run-power-tests E4:run-storage-tests E5:run-etcd-tests E6:run-clock-tests E7:run-replacement-tests E8:run-restore-tests E9:run-window-tests E10:run-fleet-tests;do
 scenario=${pair%%:*};script=${pair#*:}
 date -u
 bash "$FIELD_SCRIPTS/$script.sh"
 python3 "$FIELD_SCRIPTS/audit-suite.py" "$FIELD_ROOT/results.jsonl" --through "$scenario" > "$FIELD_ROOT/$scenario-coverage.json"
done
printf 'Matrix execution ended. Required kuberoot fixes/reruns, artifact review, article synthesis and final PR are still separate completion gates.\n'
