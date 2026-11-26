#!/bin/bash
set -euo pipefail
source /work/field/lib.sh
export PATH=/work/field:$PATH
show 'date -u; guest-command debian "ip -br address; free -m; df -h /"'
show 'guest-command debian "nft list ruleset"'
show 'date -u; guest-command client "ping -c 3 -W 2 198.19.0.1"'
show 'date -u; guest-command debian "systemctl is-active dnsmasq frr nftables; uname -r"'
