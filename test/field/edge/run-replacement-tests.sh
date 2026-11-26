#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
for lane in main talos k3s;do
 source "$FIELD_SCRIPTS/select-product.sh" "$lane"
 export HA_ROOT=$FIELD_ROOT/ha-$lane
 test ! -f "$HA_ROOT/E7/published" || continue
 if test -e "$HA_ROOT/E7";then
  printf 'Review interrupted E7 trial before reuse: %s\n' "$HA_ROOT/E7" >&2;exit 2
 fi
 if [[ $lane == talos ]];then
  bash "$FIELD_SCRIPTS/prepare-ha-talos.sh"
 else
  for id in 1 2 3;do bash "$FIELD_SCRIPTS/ha-vm.sh" start "$id";done
 fi
 export KUBECONFIG=$HA_ROOT/kubeconfig-1
 kubectl wait --for=condition=Ready nodes --all --timeout=600s
 export WORKLOAD_NODE=$(kubectl get nodes -o json | python3 -c 'import json,sys;print(next(n["metadata"]["name"] for n in json.load(sys.stdin)["items"] if any(a["address"]=="10.79.0.11" for a in n["status"]["addresses"])))')
 bash "$FIELD_SCRIPTS/deploy-workload.sh"
 # Only the guest cluster's diagnostic namespace, never workshop tenant labels.
 kubectl label namespace field pod-security.kubernetes.io/enforce=privileged --overwrite
 case $lane in
  main) export REPLACE_HOOK=$FIELD_SCRIPTS/replace-kuberoot-node.sh ARTIFACT_DIR=$FIELD_ROOT/build-main/out/amd64 KUBEROOT_VERSION=0.1.0-field-main.1b29543;;
  talos) export REPLACE_HOOK=$FIELD_SCRIPTS/replace-talos-node.sh;;
  k3s) export REPLACE_HOOK=$FIELD_SCRIPTS/replace-k3s-node.sh;;
 esac
 name=t-E7-$PRODUCT
 bash "$FIELD_SCRIPTS/capture.sh" "$name" "$FIELD_SCRIPTS/E7-scene.sh"
 python3 "$FIELD_SCRIPTS/publish-case.py" "$HA_ROOT/E7" "$name"
 for id in 1 2 3;do bash "$FIELD_SCRIPTS/ha-vm.sh" stop "$id";done
 for id in 1 2 3;do
  PRESERVED_DISK_OUTPUT=$HA_ROOT/n$id/after-E7.qcow2 bash "$FIELD_SCRIPTS/preserve-failed-disk.sh" "$HA_ROOT/n$id/system.raw" "$VM_DIR"
 done
 if test -f "$HA_ROOT/n3-retired/system.raw";then
  PRESERVED_DISK_OUTPUT=$HA_ROOT/n3-retired/retired-E7.qcow2 bash "$FIELD_SCRIPTS/preserve-failed-disk.sh" "$HA_ROOT/n3-retired/system.raw" "$VM_DIR"
 fi
 touch "$HA_ROOT/E7/published"
done
