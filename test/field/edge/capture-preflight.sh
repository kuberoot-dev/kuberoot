#!/bin/bash
set -uo pipefail
cd /work/field-edge/scripts
source ./lib.sh
set +e
show date -u
show python3 /work/field-edge/scripts/preflight.py --record
show date -u
