#!/bin/bash
# Continue only the already created Talos VM after correcting rejected config.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
D=$FIELD_ROOT/talos
G=$FIELD_SCRIPTS/gateway-command.sh
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

export VM_DIR=$D KUBECONFIG=$D/kubeconfig PRODUCT=talos-v1.14.7
export IMAGE_DIGEST=sha256:8f426aa9fca789a434b24bb64714cb53e8aa5b08d70e9c645554cac24972db4f
export SOURCE_COMMIT=55021d7c274f6bc207bf18efa2a684a31a35cd9f
bash "$FIELD_SCRIPTS/run-baseline.sh"
date -u
printf 'Talos E-B1 complete; ordered stage queue may continue.\n'
