#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
require_stand
D=${PRODUCT_DIR:-$FIELD_ROOT/kuberoot-release}
VM_DIR=${VM_DIR:-$FIELD_ROOT/vm-1}
export VM_DIR
mkdir -p "$D"
mkimage -arch amd64 -version "${KUBEROOT_VERSION:-0.1.0}" -artifacts "${ARTIFACT_DIR:-/work/artifacts}" -trust "$D/trust" -out "$D/media.img" -args 'kuberoot.nameserver=10.78.0.1'
vm_start 1 "$D/media.img"
# The authenticated API tunnel travels through the gateway VM to the DUT LAN.
if ! python3 -c 'import socket; socket.create_connection(("127.0.0.1",15000),1).close()' 2>/dev/null; then
 ssh -o BatchMode=yes -o ExitOnForwardFailure=yes -o ConnectTimeout=5 -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/work/field-edge/gateway/known_hosts -i /work/field-edge/gateway/key -p 2222 -N -f \
  -L 127.0.0.1:15000:10.78.0.11:50000 -L 127.0.0.1:16443:10.78.0.11:6443 observer@127.0.0.1
fi
export KUBECONFIG=$D/trust/admin.kubeconfig
for i in $(seq 1 120); do
 if kubectl --server=https://127.0.0.1:15000 --request-timeout=5s get disks; then break; fi
 sleep 5
done
cat > "$D/install.yaml" <<YAML
apiVersion: node.kuberoot.dev/v1alpha1
kind: Installation
metadata: {name: install}
spec: {disk: vdb, reboot: true}
YAML
kubectl --server=https://127.0.0.1:15000 create -f "$D/install.yaml"

for i in $(seq 1 120); do
 grep -q 'reboot: Restarting system' "$VM_DIR/serial.log" && break
 sleep 3
done
grep -q 'reboot: Restarting system' "$VM_DIR/serial.log"
kill "$(cat "$VM_DIR/pid")"
sleep 2
cp "$VM_DIR/serial.log" "$VM_DIR/install-serial.log"
vm_start 1
for i in $(seq 1 120); do
 if kubectl --server=https://127.0.0.1:15000 --request-timeout=5s get kubeconfig admin -o jsonpath='{.kubeconfig}' > "$D/kubeconfig.tmp"; then
  cp "$D/kubeconfig.tmp" "$D/kubeconfig"; break
 fi
 sleep 3
done
export KUBECONFIG=$D/kubeconfig
kubectl config set-cluster kuberoot --server=https://127.0.0.1:16443 --tls-server-name=10.78.0.11
kubectl config set-context --current --namespace=default
kubectl wait --for=condition=Ready nodes --all --timeout=300s
kubectl get pods -A
