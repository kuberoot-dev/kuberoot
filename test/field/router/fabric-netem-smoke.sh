#!/bin/bash
set -euo pipefail
source /work/field/lib.sh
export PATH=/work/field:$PATH
trap 'guest-command gateway "tc qdisc del dev test0 root" >/dev/null 2>&1 || true' EXIT
show 'date -u; guest-command client "ping -c 3 -W 2 198.19.0.1"'
show 'date -u; guest-command gateway "tc qdisc replace dev test0 root netem delay 50ms"'
show 'guest-command client "ping -c 3 -W 2 198.19.0.1"'
show 'date -u; guest-command gateway "tc qdisc del dev test0 root"'
show 'guest-command client "ping -c 3 -W 2 198.19.0.1"'
