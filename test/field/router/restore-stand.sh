#!/bin/bash
# Bring the stand back after the pod was recreated (all QEMU processes gone).
# Starts the kuberoot pair, phase-1 clients and the G fabric endpoints from their
# saved launch arguments, then the observer services. Competitors are started
# separately, one at a time. Gateway runtime state is re-created afterwards by
# restore-gateway.sh.
set -euo pipefail
cd /work/field
for d in /work/vms/*/; do
 [[ -f $d/pid ]] && ! kill -0 "$(cat "$d/pid")" 2>/dev/null && mv "$d/pid" "$d/pid.stale"
done
pgrep -f "qemu-system" >/dev/null && { echo "QEMU already running; not restoring" >&2; exit 1; }
for i in 1 2; do bash phase1-vm.sh router "$i"; done
for i in 1 2 5; do bash phase1-vm.sh client "$i"; done
for vm in client gateway sink; do
 python3 -c 'import json,subprocess,sys; subprocess.run(json.load(open(sys.argv[1]))["argv"],check=True)' "/work/vms/$vm/launch.json"
done
nohup python3 -m http.server 8088 --bind 127.0.0.1 --directory /work/www >/work/http.log 2>&1 </dev/null &
nohup python3 /work/field/forwarding-observer.py >/work/field/forwarding-observer.log 2>&1 </dev/null &
nohup python3 /work/field/fault-artifact-server.py >/work/field/fault-artifact-server.log 2>&1 </dev/null &
sleep 2; pgrep -af "qemu-system|http.server|observer|artifact" | cut -c1-80
