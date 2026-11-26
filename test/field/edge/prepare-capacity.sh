#!/bin/bash
# Run only at a stage boundary, with all DUT VMs stopped.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
require_stand
if ! command -v mcopy >/dev/null || ! command -v nft >/dev/null; then
 apt-get -o Dir::Cache::archives=/work/apt/archives -o Dir::State::lists=/work/apt/lists update
 apt-get -o Dir::Cache::archives=/work/apt/archives -o Dir::State::lists=/work/apt/lists install -y --no-install-recommends mtools nftables zstd
fi
python3 "$FIELD_SCRIPTS/cleanup-reproducible-media.py"
df -h /work
