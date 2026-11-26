#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
require_stand
D=$FIELD_ROOT/kuberoot-release
mkdir -p "$D"
mkimage -arch amd64 -version 0.1.0 -artifacts /work/artifacts -trust "$D/trust" -out "$D/media.img" -args 'kuberoot.nameserver=10.78.0.1'
vm_start 1 "$D/media.img"
# The authenticated API tunnel travels through the gateway VM to the DUT LAN.
ssh -o BatchMode=yes -o ConnectTimeout=5 -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/work/field-edge/gateway/known_hosts -i /work/field-edge/gateway/key -p 2222 -N -f \
 -L 127.0.0.1:15000:10.78.0.11:50000 -L 127.0.0.1:16443:10.78.0.11:6443 observer@127.0.0.1
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
