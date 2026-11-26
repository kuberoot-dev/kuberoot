#!/bin/bash
# Published v0.1.0 binaries, explicitly distinct lab-trust repack from Phase 1.
# A private vendor key is neither available nor required for intentional faults.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
export PRODUCT_DIR=$FIELD_ROOT/kuberoot-release-testkey VM_DIR=$FIELD_ROOT/vm-release-testkey
export ARTIFACT_DIR=/work/series/0.1.0-capture.1 KUBEROOT_VERSION=0.1.0-capture.1
bash "$FIELD_SCRIPTS/prepare-kuberoot.sh"
bash "$FIELD_SCRIPTS/stop-dut.sh" "$VM_DIR"
source "$FIELD_SCRIPTS/select-product.sh" release-testkey
bash "$FIELD_SCRIPTS/prepare-workload-golden.sh"
