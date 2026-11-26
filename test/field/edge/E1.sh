#!/bin/bash
# 60-minute WAN blackhole with hard reset at minute 10. Requires cached W.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
: "${KUBECONFIG:?}" "${RUN_NAME:?unique product-run ID}" "${VM_DIR:?owned DUT directory}"
[[ $VM_DIR == "$FIELD_ROOT/"* && $RUN_NAME =~ ^[A-Za-z0-9_.-]+$ ]] || exit 2
D=$FIELD_ROOT/results/$RUN_NAME
mkdir "$D"
G=$FIELD_SCRIPTS/gateway-command.sh
# Observer and credentials were copied to the gateway during fixture preparation.
show date -u
show kubectl -n field get pods -o wide
"$G" "nohup python3 /home/observer/observer.py --http http://10.78.0.11:30080 --append http://10.78.0.11:30081 --kubeconfig /home/observer/kubeconfig --seconds 4020 --output /home/observer/$RUN_NAME.jsonl >/home/observer/$RUN_NAME.log 2>&1 </dev/null &"
sleep 60
show "$FIELD_SCRIPTS/wan-impairment.sh" blackhole
fault=$(date +%s)
printf '%s\n' "$fault" > "$D/fault-epoch"
sleep 600
show date -u
date +%s > "$D/power-cut-epoch"
show kill -9 "$(cat "$VM_DIR/pid")"
cp "$VM_DIR/serial.log" "$D/before-cut-serial.log"
show vm_start 1
show date -u
# Keep the blackhole for a full hour measured from the fault, including reboot.
remain=$((fault+3600-$(date +%s)))
if ((remain>0)); then sleep "$remain"; fi
show date -u
show kubectl -n field run uncached-image --image=registry.k8s.io/field-deliberately-missing:never-published --restart=Never
sleep 30
show kubectl -n field get pod uncached-image -o yaml
show "$FIELD_SCRIPTS/wan-impairment.sh" clean
sleep 340
show date -u
"$G" "cat /home/observer/$RUN_NAME.jsonl" > "$D/observer.jsonl"
"$G" 'curl --fail --max-time 10 http://10.78.0.11:30081/records' > "$D/recovered-records"
python3 "$FIELD_SCRIPTS/check-records.py" "$D/observer.jsonl" "$D/recovered-records" | tee "$D/durability.json"
cp "$VM_DIR/serial.log" "$D/recovery-serial.log"

python3 "$FIELD_SCRIPTS/analyse-observer.py" "$D/observer.jsonl" --fault-epoch "$(cat "$D/power-cut-epoch")" | tee "$D/recovery.json"
