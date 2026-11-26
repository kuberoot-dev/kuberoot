#!/bin/bash
# Source this file. Paths contain credentials; do not print kubeconfig contents.
export FIELD_ROOT=/work/field-edge
unset SEED_MEDIA BOOT_ISO RTC_BASE BLOCK_FAULTS NODE_KUBECONFIG
case "${1:?release|main|health|k3s|talos}" in
 release)
  export PRODUCT=kuberoot-v0.1.0 VM_DIR=$FIELD_ROOT/vm-1 KUBECONFIG=$FIELD_ROOT/kuberoot-release/kubeconfig
  export SOURCE_COMMIT=ffcc71a4c6c7d3aa36c37221f0cd1d19c22e99b0 IMAGE_DIGEST=sha256:29b12ff8abc63ebd05a085b6a1f2c9f131faf7d9ae57f92d6a5e1cc250ef01ce
  export NODE_KUBECONFIG=$FIELD_ROOT/kuberoot-release/trust/admin.kubeconfig ;;
 release-testkey)
  export PRODUCT=kuberoot-v0.1.0-testkey VM_DIR=$FIELD_ROOT/vm-release-testkey KUBECONFIG=$FIELD_ROOT/kuberoot-release-testkey/kubeconfig
  export SOURCE_COMMIT=ffcc71a4c6c7d3aa36c37221f0cd1d19c22e99b0
  export IMAGE_DIGEST=sha256:$(sha256sum /work/series/0.1.0-capture.1/rootfs.squashfs | cut -d' ' -f1)
  export NODE_KUBECONFIG=$FIELD_ROOT/kuberoot-release-testkey/trust/admin.kubeconfig ;;
 main)
  export PRODUCT=kuberoot-main-1b29543 VM_DIR=$FIELD_ROOT/vm-main KUBECONFIG=$FIELD_ROOT/kuberoot-main/kubeconfig
  export SOURCE_COMMIT=1b29543967ae6fce971f0bf818000a6b04374b52 IMAGE_DIGEST=sha256:918c2b9c5d73fe103e1574de1d9e61815ba1b161ce52e5cad9c907be68ff2de0
  export NODE_KUBECONFIG=$FIELD_ROOT/kuberoot-main/trust/admin.kubeconfig ;;
 health)
  export PRODUCT=kuberoot-health-52db7b1 VM_DIR=$FIELD_ROOT/vm-health KUBECONFIG=$FIELD_ROOT/kuberoot-health/kubeconfig
  export SOURCE_COMMIT=52db7b1fba8dc386e519cd137d6bf42afce6c756
  test "$(cat "$FIELD_ROOT/build-health/SOURCE_COMMIT")" = "$SOURCE_COMMIT" || return 2
  export IMAGE_DIGEST=sha256:$(sha256sum "$FIELD_ROOT/build-health/out/amd64/rootfs.squashfs" | cut -d' ' -f1)
  export NODE_KUBECONFIG=$FIELD_ROOT/kuberoot-health/trust/admin.kubeconfig ;;
 k3s)
  export PRODUCT=k3s-v1.37.2-k3s1 VM_DIR=$FIELD_ROOT/k3s KUBECONFIG=$FIELD_ROOT/k3s/kubeconfig SEED_MEDIA=$FIELD_ROOT/k3s/seed.iso
  export SOURCE_COMMIT=f6973cb850e9b245585363d15bde80904ac862fd
  export IMAGE_DIGEST=sha256:$(sha256sum "$FIELD_ROOT/downloads/ubuntu-24.04-server-cloudimg-amd64.img" | cut -d' ' -f1) ;;
 talos)
  export PRODUCT=talos-v1.14.7 VM_DIR=$FIELD_ROOT/talos KUBECONFIG=$FIELD_ROOT/talos/kubeconfig
  export SOURCE_COMMIT=55021d7c274f6bc207bf18efa2a684a31a35cd9f IMAGE_DIGEST=sha256:8f426aa9fca789a434b24bb64714cb53e8aa5b08d70e9c645554cac24972db4f ;;
 *) return 2 ;;
esac
export PYTHON_IMAGE=$(python3 -c 'import json;print(json.load(open("/work/field-edge/downloads/workload-image.json"))["image"])')

if test -n "${NODE_KUBECONFIG:-}"; then
 node_cluster=$(KUBECONFIG=$NODE_KUBECONFIG kubectl config view -o jsonpath='{.clusters[0].name}')
 KUBECONFIG=$NODE_KUBECONFIG kubectl config set-cluster "$node_cluster" --server=https://127.0.0.1:15000 >/dev/null
fi
