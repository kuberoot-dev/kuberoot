#!/bin/bash
# Two complete three-node topologies run sequentially within the guest RAM cap.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
for lane in main k3s; do
 source "$FIELD_SCRIPTS/select-product.sh" "$lane"
 export HA_ROOT=$FIELD_ROOT/ha-$lane
 unset ETCD_LEADER
 if [[ $lane == main ]]; then
  export ARTIFACT_DIR=$FIELD_ROOT/build-main/out/amd64 KUBEROOT_VERSION=0.1.0-field-main.1b29543
  bash "$FIELD_SCRIPTS/prepare-ha-kuberoot.sh"
 else
  bash "$FIELD_SCRIPTS/prepare-ha-k3s.sh"
  export ETCD_LEADER=$(python3 -c 'import json,os;print(json.load(open(os.environ["HA_ROOT"]+"/observed-leader.json"))["leader_node"])')
 fi
 name=t-E5-$PRODUCT
 bash "$FIELD_SCRIPTS/capture.sh" "$name" "$FIELD_SCRIPTS/E5-scene.sh"
 python3 "$FIELD_SCRIPTS/publish-case.py" "$HA_ROOT/E5" "$name"
 for id in 1 2 3; do bash "$FIELD_SCRIPTS/ha-vm.sh" stop "$id";done
 # Preserve exact node state as verified deltas. E7 materializes raw disks.
 for id in 1 2 3;do
  bash "$FIELD_SCRIPTS/preserve-failed-disk.sh" "$HA_ROOT/n$id/system.raw" "$VM_DIR"
 done
done
