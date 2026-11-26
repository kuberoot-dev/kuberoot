#!/bin/bash
# 60-minute WAN blackhole with hard reset at minute 10. Requires cached W.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
: "${KUBECONFIG:?}" "${RUN_NAME:?unique product-run ID}" "${VM_DIR:?owned DUT directory}"
[[ $VM_DIR == "$FIELD_ROOT/"* && $RUN_NAME =~ ^[A-Za-z0-9_.-]+$ ]] || exit 2
D=$FIELD_ROOT/results/$RUN_NAME
mkdir "$D"
G=$FIELD_SCRIPTS/gateway-command.sh
# Refresh credentials for this product even when its cached golden is reused.
bash "$FIELD_SCRIPTS/prepare-observer.sh"
show export "KUBECONFIG=$KUBECONFIG"
show date -u
show "$G" '/usr/sbin/sysctl net.netfilter.nf_conntrack_count net.netfilter.nf_conntrack_max; test "$(cat /proc/sys/net/netfilter/nf_conntrack_max)" -ge 65536'
"$G" "nohup python3 /home/observer/gateway-monitor.py --seconds 4200 --stop-file /home/observer/$RUN_NAME-monitor.stop --output /home/observer/$RUN_NAME-monitor.jsonl >/home/observer/$RUN_NAME-monitor.log 2>&1 </dev/null &"
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
show "$FIELD_SCRIPTS/boot-node.sh" 1
# Record the actual serial stream of the recovery, separately from the API view.
asciinema rec --cols 110 --rows 30 -q -c "python3 '$FIELD_SCRIPTS/serial-follow.py' '$VM_DIR/serial.log' --stop-file '$D/serial.stop' --max-seconds 180" "$D/recovery.cast" > "$D/serial-recorder.log" 2>&1 &
serial_recorder=$!
vga_recorder=
if [[ ${PRODUCT:-} == talos* ]]; then
 python3 "$FIELD_SCRIPTS/vga-capture.py" --qmp "$VM_DIR/qmp.sock" --output "$D/vga" --stop-file "$D/serial.stop" --seconds 180 > "$D/vga-capture.log" 2>&1 &
 vga_recorder=$!
fi
show date -u
# Keep the blackhole for a full hour measured from the fault, including reboot.
remain=$((fault+3600-$(date +%s)))
if ((remain>0)); then sleep "$remain"; fi
show date -u
show kubectl -n field run uncached-image --image=registry.k8s.io/field-deliberately-missing:never-published --restart=Never
sleep 30
kubectl -n field get pod uncached-image -o json > "$D/missing-image.json"
show kubectl -n field get pod uncached-image -o yaml
show "$FIELD_SCRIPTS/wan-impairment.sh" clean
sleep 340
show date -u
"$G" "touch /home/observer/$RUN_NAME-monitor.stop"
sleep 2
"$G" "cat /home/observer/$RUN_NAME-monitor.jsonl" > "$D/gateway-monitor.jsonl"
"$G" "cat /home/observer/$RUN_NAME.jsonl" > "$D/observer.jsonl"
"$G" 'curl --fail --max-time 10 http://10.78.0.11:30081/records' > "$D/recovered-records"
python3 "$FIELD_SCRIPTS/check-records.py" "$D/observer.jsonl" "$D/recovered-records" > "$D/durability.json" || true
show cat "$D/durability.json"
cp "$VM_DIR/serial.log" "$D/recovery-serial.log"

python3 "$FIELD_SCRIPTS/analyse-observer.py" "$D/observer.jsonl" --fault-epoch "$(cat "$D/power-cut-epoch")" | tee "$D/recovery.json"

wait "$serial_recorder"
if [[ -n $vga_recorder ]]; then wait "$vga_recorder"; fi
python3 /work/scripts/render.py "$D/recovery.cast" > "$D/recovery-timing.json"
python3 "$FIELD_SCRIPTS/bound-cast.py" "$D/recovery.retimed.cast" > "$D/recovery-speed.json"
hold=$(python3 -c 'import json,sys;print(max(5,15.1-json.load(open(sys.argv[1]))["playback_seconds"]))' "$D/recovery-timing.json")
agg -q --font-size 16 --theme monokai --last-frame-duration "$hold" "$D/recovery.retimed.cast" "$D/recovery.gif"
