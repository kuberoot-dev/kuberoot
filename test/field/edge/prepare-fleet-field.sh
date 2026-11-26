#!/bin/bash
# E10 fixture, executed only after earlier mandatory rows have finished.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
require_stand
: "${PYTHON_IMAGE:?pinned workload image}"
F=$FIELD_ROOT/fleet-field
mkdir "$F"
# A clean, immutable gateway backing is made at a stage boundary. Never clone
# the running gateway disk, and never touch a different workshop pod or VM.
python3 - "$F" <<'PY'
import json,pathlib,sys
out=pathlib.Path(sys.argv[1]);gateway=None
for p in pathlib.Path('/proc').glob('[0-9]*/cmdline'):
 try:a=p.read_bytes().decode().rstrip('\0').split('\0')
 except (OSError,UnicodeError):continue
 if not a[0].endswith('qemu-system-x86_64'):continue
 if '-name' not in a or a[a.index('-name')+1]!='edge-field-gateway':raise SystemExit('Stop earlier DUTs before preparing fleet topology')
 gateway=a
assert gateway
(out/'shared-gateway-command.private.json').write_text(json.dumps(gateway))
PY
"$FIELD_SCRIPTS/gateway-command.sh" 'sudo shutdown -h now' || true
for i in $(seq 1 120); do
 pid=$(cat "$FIELD_ROOT/gateway/pid")
 if ! test -e "/proc/$pid/stat" || [[ $(awk '{print $3}' "/proc/$pid/stat") == Z ]]; then break;fi
 sleep 1
done
python3 - "$FIELD_ROOT/gateway/pid" <<'PY'
import pathlib,sys
p=pathlib.Path('/proc')/pathlib.Path(sys.argv[1]).read_text().strip()/'stat'
assert not p.exists() or p.read_text().split()[2]=='Z','gateway did not stop'
PY
qemu-img convert -c -f qcow2 -O qcow2 "$FIELD_ROOT/gateway/system.qcow2" "$F/gateway-base.qcow2"
qemu-img compare -f qcow2 -F qcow2 "$FIELD_ROOT/gateway/system.qcow2" "$F/gateway-base.qcow2"
python3 - "$F/shared-gateway-command.private.json" <<'PY'
import json,subprocess,sys
subprocess.run(json.load(open(sys.argv[1])),check=True)
PY
# Unprivileged site NAT gateways share immutable base blocks, not active state.
for id in 1 2 3; do
 bash "$FIELD_SCRIPTS/fleet-field-vm.sh" gateway "$id"
 for attempt in $(seq 1 120); do
  if "$FIELD_SCRIPTS/fleet-gateway-command.sh" "$id" 'true'; then break;fi
  sleep 2
 done
 "$FIELD_SCRIPTS/fleet-gateway-command.sh" "$id" "sudo sed -i 's/52:54:00:78:00:01/52:54:00:81:00:0$id/' /etc/dnsmasq.d/field.conf; sudo systemctl restart dnsmasq; sudo nft flush chain inet impairment forward; sudo nft delete chain inet impairment dns_output 2>/dev/null || true; sudo tc qdisc del dev wan0 root 2>/dev/null || true; sudo tc qdisc del dev wan0 ingress 2>/dev/null || true"
 ssh -o BatchMode=yes -o ExitOnForwardFailure=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="$F/gateway-known-hosts" -i "$FIELD_ROOT/gateway/key" -p "2230$id" -N -f \
  -L "127.0.0.1:2951$id:10.78.0.11:50000" -L "127.0.0.1:2941$id:10.78.0.11:6443" observer@127.0.0.1
done
mkimage -arch amd64 -version 0.1.0-field-main.1b29543 -artifacts "$FIELD_ROOT/build-main/out/amd64" -trust "$F/trust" -out "$F/media.img" -args 'kuberoot.nameserver=10.78.0.1'
mkimage -arch amd64 -version 0.1.0-field-main.1b29543 -artifacts "$FIELD_ROOT/build-main/out/amd64" -trust "$F/trust" -out "$F/center-media.img" -args 'kuberoot.nameserver=10.0.2.3'
export KUBECONFIG=$F/trust/admin.kubeconfig
for id in 0 1 2 3; do
 role=site;port=2951$id;[[ $id != 0 ]] || { role=center;port=29500; }
 D=$F/$role-$id
 media=$F/media.img;[[ $id != 0 ]] || media=$F/center-media.img
 bash "$FIELD_SCRIPTS/fleet-field-vm.sh" "$role" "$id" "$media"
 for attempt in $(seq 1 150); do
  if kubectl --server="https://127.0.0.1:$port" --request-timeout=5s get disks; then break;fi
  sleep 2
 done
 kubectl --server="https://127.0.0.1:$port" create -f - <<'YAML'
apiVersion: node.kuberoot.dev/v1alpha1
kind: Installation
metadata: {name: field-install}
spec: {disk: vdb, reboot: true}
YAML
 for attempt in $(seq 1 180); do
  if grep -q 'reboot: Restarting system' "$D/serial.log"; then break;fi
  sleep 2
 done
 grep -q 'reboot: Restarting system' "$D/serial.log"
 python3 "$FIELD_SCRIPTS/qmp.py" "$D/qmp.sock" quit
 sleep 2
 cp "$D/serial.log" "$D/install-serial.log"
 bash "$FIELD_SCRIPTS/fleet-field-vm.sh" "$role" "$id"
 for attempt in $(seq 1 150); do
  if kubectl --server="https://127.0.0.1:$port" get kubeconfig admin -o jsonpath='{.kubeconfig}' > "$F/$role-$id-kubeconfig"; then break;fi
  sleep 2
 done
 kubeport=2941$id;tls=10.78.0.11;[[ $id != 0 ]] || { kubeport=29443;tls=10.0.2.15; }
 KUBECONFIG=$F/$role-$id-kubeconfig kubectl config set-cluster kuberoot --server="https://127.0.0.1:$kubeport" --tls-server-name="$tls" >/dev/null
 KUBECONFIG=$F/$role-$id-kubeconfig kubectl wait --for=condition=Ready nodes --all --timeout=300s
 if [[ $id != 0 ]]; then
  KUBECONFIG=$F/$role-$id-kubeconfig bash "$FIELD_SCRIPTS/deploy-workload.sh"
 fi
done
cp "$F/center-0-kubeconfig" "$F/center-kubeconfig"
export KUBECONFIG=$F/center-kubeconfig
kubectl config view --raw -o json | python3 -c 'import base64,json,sys;sys.stdout.buffer.write(base64.b64decode(json.load(sys.stdin)["clusters"][0]["cluster"]["certificate-authority-data"]))' > "$F/center-client-ca.crt"
for id in 1 2; do
 nohup bash "$FIELD_SCRIPTS/fleet-manager.sh" "$id" > "$F/manager-$id-supervisor.log" 2>&1 </dev/null &
 echo "$!" > "$F/manager-$id-supervisor.pid"
done
for attempt in $(seq 1 90); do
 if kubectl get fleetnodes; then break;fi
 sleep 2
done
for id in 1 2 3; do
 kubectl create -f - <<YAML
apiVersion: fleet.kuberoot.dev/v1alpha1
kind: FleetNode
metadata: {name: site-$id, labels: {field-suite: edge}}
YAML
 for attempt in $(seq 1 90); do
  if kubectl -n kube-system get secret "fleet-enroll-site-$id" -o jsonpath='{.data.enrollment\.yaml}' | base64 -d > "$F/enroll-$id.private.yaml" && test -s "$F/enroll-$id.private.yaml";then break;fi
  sleep 2
 done
 KUBECONFIG=$F/trust/admin.kubeconfig kubectl --server="https://127.0.0.1:2951$id" apply -f "$F/enroll-$id.private.yaml"
done
for attempt in $(seq 1 120); do
 count=$(kubectl get fleetnodes -o json | python3 -c 'import json,sys;print(sum(x.get("status",{}).get("phase")=="Connected" for x in json.load(sys.stdin)["items"]))')
 test "$count" != 3 || break
 sleep 2
done
test "$count" = 3
kubectl get fleetnodes -o json > "$F/nodes-before.json"
kubectl -n kube-system get lease kuberoot-fleet -o json > "$F/lease-before.json"
printf 'Three real sites, three isolated NAT VMs, center datastore VM and two supervised managers ready.\n' > "$F/prepared"
