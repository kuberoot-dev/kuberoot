#!/bin/bash
# Same DUT allocation and multicast LAN. Run after stopping the prior product.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
require_stand
D=$FIELD_ROOT/talos
mkdir -p "$D"
cat > "$D/patch.yaml" <<'YAML'
apiVersion: v1alpha1
kind: HostnameConfig
auto:
  $patch: delete
hostname: edge-talos
---
apiVersion: v1alpha1
kind: KubeNodeConfig
taints:
  node-role.kubernetes.io/control-plane:
    $patch: delete
YAML
talosctl gen config edge-field https://10.78.0.11:6443 --install-disk /dev/vda \
 --talos-version v1.14.7 --kubernetes-version 1.37.1 --additional-sans 127.0.0.1 \
 --config-patch @"$D/patch.yaml" --output "$D/config"
VM_DIR=$D BOOT_ISO=$FIELD_ROOT/downloads/talos-v1.14.7-metal-amd64.iso vm_start 1
# Tools and secrets stay in the dedicated gateway VM; nothing printed publicly.
scp -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="$FIELD_ROOT/gateway/known_hosts" \
 -i "$FIELD_ROOT/gateway/key" -P 2222 /work/bin/talosctl "$D/config/controlplane.yaml" "$D/config/talosconfig" observer@127.0.0.1:/home/observer/
G=$FIELD_SCRIPTS/gateway-command.sh
for i in $(seq 1 120); do
 if "$G" '/home/observer/talosctl --nodes 10.78.0.11 apply-config --insecure --file /home/observer/controlplane.yaml'; then break; fi
 sleep 5
done
for i in $(seq 1 120); do
 if "$G" '/home/observer/talosctl --talosconfig /home/observer/talosconfig --nodes 10.78.0.11 --endpoints 10.78.0.11 version'; then break; fi
 sleep 5
done
bootstrapped=false
for i in $(seq 1 120); do
 if "$G" '/home/observer/talosctl --talosconfig /home/observer/talosconfig --nodes 10.78.0.11 --endpoints 10.78.0.11 bootstrap'; then bootstrapped=true; break; fi
 sleep 5
done
$bootstrapped || { echo 'Talos did not accept bootstrap; inspect services/install logs' >&2; exit 1; }
for i in $(seq 1 120); do
 if "$G" '/home/observer/talosctl --talosconfig /home/observer/talosconfig --nodes 10.78.0.11 --endpoints 10.78.0.11 kubeconfig --merge=false -' > "$D/kubeconfig"; then break; fi
 sleep 5
done
chmod 600 "$D/kubeconfig"
export KUBECONFIG=$D/kubeconfig
cluster=$(kubectl config view -o jsonpath='{.clusters[0].name}')
kubectl config set-cluster "$cluster" --server=https://127.0.0.1:16443 --tls-server-name=10.78.0.11
kubectl config set-context --current --namespace=default
for i in $(seq 1 120); do
 if kubectl --request-timeout=5s get node edge-talos; then break; fi
 sleep 5
done
kubectl wait --for=condition=Ready node/edge-talos --timeout=600s
kubectl get pods -A
# Baseline must boot from the installed disk, with installation ISO detached.
bash "$FIELD_SCRIPTS/stop-dut.sh" "$D"
cp "$D/serial.log" "$D/install-serial.log"
VM_DIR=$D vm_start 1
for i in $(seq 1 120); do
 if kubectl --request-timeout=5s get --raw /readyz; then break; fi
 sleep 5
done
kubectl wait --for=condition=Ready node/edge-talos --timeout=300s
