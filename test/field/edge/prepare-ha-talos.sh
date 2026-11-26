#!/bin/bash
# Three real Talos control planes. Only machine API calls, no guest shell.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
require_stand
: "${HA_ROOT:?owned ha-talos directory}"
[[ $HA_ROOT == /work/field-edge/ha-* ]]
mkdir "$HA_ROOT"
umask 077
talosctl gen config field-ha https://10.79.0.11:6443 --install-disk /dev/vda --talos-version v1.14.7 --kubernetes-version 1.37.1 --additional-sans 127.0.0.1,10.79.0.11,10.79.0.12,10.79.0.13 --output "$HA_ROOT/config" > "$HA_ROOT/gen.log" 2>&1
for id in 1 2 3; do
 python3 "$FIELD_SCRIPTS/talos-ha-patch.py" "$id" > "$HA_ROOT/network-$id.yaml"
 talosctl machineconfig patch "$HA_ROOT/config/controlplane.yaml" --patch @"$HA_ROOT/network-$id.yaml" --output "$HA_ROOT/controlplane-$id.private.yaml" > "$HA_ROOT/patch-$id.log" 2>&1
 talosctl validate --config "$HA_ROOT/controlplane-$id.private.yaml" --mode metal
 HA_BOOT_ISO=$FIELD_ROOT/downloads/talos-v1.14.7-metal-amd64.iso bash "$FIELD_SCRIPTS/ha-vm.sh" start "$id"
 for attempt in $(seq 1 150); do
  if talosctl --nodes "127.0.0.1:25${id}00" apply-config --insecure --file "$HA_ROOT/controlplane-$id.private.yaml";then break;fi
  sleep 2
 done
done
export TALOSCONFIG=$HA_ROOT/config/talosconfig
for id in 1 2 3; do
 for attempt in $(seq 1 180); do
  if talosctl --endpoints "127.0.0.1:25${id}00" --nodes "10.79.0.1$id" version > "$HA_ROOT/version-$id.txt";then break;fi
  sleep 3
 done
done
talosctl --endpoints 127.0.0.1:25100 --nodes 10.79.0.11 bootstrap
for id in 1 2 3; do
 for attempt in $(seq 1 120); do
  if talosctl --endpoints "127.0.0.1:25${id}00" --nodes "10.79.0.1$id" kubeconfig --merge=false - > "$HA_ROOT/kubeconfig-$id";then break;fi
  sleep 3
 done
 cluster=$(KUBECONFIG=$HA_ROOT/kubeconfig-$id kubectl config view -o jsonpath='{.clusters[0].name}')
 KUBECONFIG=$HA_ROOT/kubeconfig-$id kubectl config set-cluster "$cluster" --server="https://127.0.0.1:26${id}43" --tls-server-name="10.79.0.1$id" >/dev/null
 KUBECONFIG=$HA_ROOT/kubeconfig-$id kubectl wait --for=condition=Ready nodes --all --timeout=600s
 KUBECONFIG=$HA_ROOT/kubeconfig-$id kubectl get nodes -o json > "$HA_ROOT/nodes-$id.json"
done
# Remove ISO from the running definition only after installed boot is confirmed.
for id in 1 2 3; do
 talosctl --endpoints "127.0.0.1:25${id}00" --nodes "10.79.0.1$id" shutdown --wait --timeout=3m
 bash "$FIELD_SCRIPTS/ha-vm.sh" start "$id"
done
KUBECONFIG=$HA_ROOT/kubeconfig-1 kubectl wait --for=condition=Ready nodes --all --timeout=600s
