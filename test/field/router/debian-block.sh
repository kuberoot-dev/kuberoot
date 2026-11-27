#!/bin/bash
# Debian block: boot the configured Debian router on G, then R4 VLAN isolation,
# R11 storage (IOPS, 95/100 %, EIO, power cut after an acked write), R5 SYN flood.
set -uo pipefail
cd /work/field
for other in r11 openwrt vyos; do [[ -e /work/vms/$other/pid ]] && { echo "$other is running"; exit 1; }; done
[[ -e /work/vms/debian/pid ]] || bash fabric-competitor.sh debian
for i in $(seq 100); do /work/field/guest-command debian true 2>/dev/null && break; sleep 3; done
/work/field/guest-command debian 'ip -br addr; nft list ruleset | head -20; ip route' | tee raw/debian-block7-state.txt
/work/field/guest-command client 'ping -c 3 -W 1 198.19.0.1 | tail -1'
for row in R4-debian-isolation R11-debian-storage R11-debian-eio R5-debian-synflood; do
 printf '#!/bin/bash\nsource /work/field/lib.sh\nshow "python3 /work/field/%s.py"\n' "$row" > scene-t-$row-run.sh
 DEFER_RENDER=1 bash capture.sh t-$row-run scene-t-$row-run.sh > raw/$row-run.capture.log 2>&1
 echo "$row exit $(cat casts/t-$row-run.exit 2>/dev/null)"
done
echo debian-block-done
