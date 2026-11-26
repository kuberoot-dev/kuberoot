#!/bin/bash
set -euo pipefail
source /work/field/lib.sh
export PATH=/work/field:$PATH
show 'date -u; guest-command client "ip -br address; ip route"'
show 'guest-command gateway "ip -br address; ip route"'
show 'date -u; guest-command client "ping -c 3 -W 2 198.18.33.2"'
show 'date -u; guest-command client "iperf3 -c 198.18.33.2 -P 8 -t 5"'
