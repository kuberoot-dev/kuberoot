#!/bin/bash
# Sequential E-B1 products. A setup failure stops here for diagnosis, not a pass.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
while ! test -s "$FIELD_ROOT/results.jsonl"; do sleep 10; done
python3 - "$FIELD_ROOT/results/E-B1-kuberoot-release/result.json" <<'PY'
import json,sys
r=json.load(open(sys.argv[1]));assert r['status']=='measured',r
PY
# Do not replace the capture script while its initial recording is open.
cp "$FIELD_SCRIPTS/capture-next.sh" "$FIELD_SCRIPTS/capture.sh"
bash "$FIELD_SCRIPTS/stop-dut.sh" "$FIELD_ROOT/vm-1"
date -u
bash "$FIELD_SCRIPTS/build-main.sh"
export VM_DIR=$FIELD_ROOT/vm-main PRODUCT_DIR=$FIELD_ROOT/kuberoot-main
export KUBEROOT_VERSION=0.1.0-field-main.1b29543 ARTIFACT_DIR=$FIELD_ROOT/build-main/out/amd64
bash "$FIELD_SCRIPTS/prepare-kuberoot.sh"
export KUBECONFIG=$PRODUCT_DIR/kubeconfig PRODUCT=kuberoot-main-1b29543
export IMAGE_DIGEST=sha256:$(sha256sum "$ARTIFACT_DIR/rootfs.squashfs" | cut -d' ' -f1)
export SOURCE_COMMIT=1b29543967ae6fce971f0bf818000a6b04374b52
bash "$FIELD_SCRIPTS/run-baseline.sh"
bash "$FIELD_SCRIPTS/stop-dut.sh" "$VM_DIR"
# Compiler caches and unpacked staging root are reproducible scratch, not evidence.
rm -rf /work/field-edge/build-main/gocache /work/field-edge/build-main/rootfs
unset PRODUCT_DIR KUBEROOT_VERSION ARTIFACT_DIR
bash "$FIELD_SCRIPTS/prepare-k3s.sh"
export VM_DIR=$FIELD_ROOT/k3s KUBECONFIG=$FIELD_ROOT/k3s/kubeconfig PRODUCT=k3s-v1.37.2-k3s1
export IMAGE_DIGEST=sha256:$(sha256sum "$FIELD_ROOT/downloads/ubuntu-24.04-server-cloudimg-amd64.img" | cut -d' ' -f1)
export SOURCE_COMMIT=f6973cb850e9b245585363d15bde80904ac862fd
bash "$FIELD_SCRIPTS/run-baseline.sh"
bash "$FIELD_SCRIPTS/stop-dut.sh" "$VM_DIR"
bash "$FIELD_SCRIPTS/prepare-talos.sh"
export VM_DIR=$FIELD_ROOT/talos KUBECONFIG=$FIELD_ROOT/talos/kubeconfig PRODUCT=talos-v1.14.7
export IMAGE_DIGEST=sha256:8f426aa9fca789a434b24bb64714cb53e8aa5b08d70e9c645554cac24972db4f
export SOURCE_COMMIT=55021d7c274f6bc207bf18efa2a684a31a35cd9f
bash "$FIELD_SCRIPTS/run-baseline.sh"
date -u
printf 'E-B1 product loop finished. Continue with E-B2; suite is NOT complete.\n'
