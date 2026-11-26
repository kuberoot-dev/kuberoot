#!/bin/bash
# Fresh installation of the fixed A image; never alter the original main lane.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
test -f "$FIELD_ROOT/build-health/built"
export PRODUCT_DIR=$FIELD_ROOT/kuberoot-health VM_DIR=$FIELD_ROOT/vm-health
export ARTIFACT_DIR=$FIELD_ROOT/build-health/out/amd64 KUBEROOT_VERSION=0.1.0-field-health.52db7b1
[[ ! -e $VM_DIR && ! -e $PRODUCT_DIR ]] || { printf 'Review partial health-golden setup before retrying\n' >&2;exit 2; }
bash "$FIELD_SCRIPTS/prepare-kuberoot.sh"
bash "$FIELD_SCRIPTS/stop-dut.sh" "$VM_DIR"
source "$FIELD_SCRIPTS/select-product.sh" health
bash "$FIELD_SCRIPTS/prepare-workload-golden.sh"
