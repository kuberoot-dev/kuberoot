#!/bin/bash
# Boot the selected owned DUT directory using the shared checked QEMU recipe.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
vm_start "${1:?node ID}"
