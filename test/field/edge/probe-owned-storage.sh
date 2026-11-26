#!/bin/bash
# Dedicated scratch only; never open a measured VM image for writing.
set -euo pipefail
source /work/field-edge/scripts/lib.sh
scratch=$(mktemp /work/field-edge/storage-probe-XXXXXX.raw)
trap 'rm -f -- "$scratch"' EXIT
show date -u
show df -h /work
show qemu-img create -f raw "$scratch" 32G
for offset in 339306496 339314688 340477952 340486144 341682176 341690368 342824960 342833152; do
 show qemu-io -f raw -t none -c "write -P 0x5a $offset 1048576" -c flush -c "read -P 0x5a $offset 1048576" "$scratch"
done
show date -u
