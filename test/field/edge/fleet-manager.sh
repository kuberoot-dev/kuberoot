#!/bin/bash
# A process supervisor inside the owned pod, backed by the center VM's API.
# Two independent instances have distinct advertised ports and Lease identities.
set -euo pipefail
id=${1:?1|2};[[ $id == 1 || $id == 2 ]] || exit 2
D=/work/field-edge/fleet-field
while true; do
 /work/field-edge/build-main/out/amd64/kuberoot-fleet --kubeconfig "$D/center-kubeconfig" --listen ":1744$((id+2))" --servers "10.0.2.2:1744$((id+2))" --identity "field-manager-$id" --user-client-ca-file "$D/center-client-ca.crt" >> "$D/manager-$id.log" 2>&1 &
 child=$!;echo "$child" > "$D/manager-$id.pid"
 wait "$child" || true
 sleep 2
done
