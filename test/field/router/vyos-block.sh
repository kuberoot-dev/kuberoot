#!/bin/bash
# VyOS block: boot the installed VyOS on G with a fourth NIC on the transit
# segment, then R9, R8, R3 and R2 in that order, each recorded.
set -uo pipefail
cd /work/field
for other in r11 openwrt debian; do [[ -e /work/vms/$other/pid ]] && { echo "$other is running"; exit 1; }; done
if [[ ! -e /work/vms/vyos/pid ]]; then
 INSTALLED=1 FIELD_EXTRA_NET="-netdev socket,id=transit,mcast=230.0.0.3:12003,localaddr=127.0.0.1 -device virtio-net-pci,romfile=,netdev=transit,mac=52:54:00:30:00:04" bash fabric-competitor.sh vyos
fi
for i in $(seq 100); do /work/field/vyos-command true 2>/dev/null && break; sleep 3; done
/work/field/vyos-command 'ip -br link; ip -br addr' | tee raw/vyos-block6-links.txt
tif=$(/work/field/vyos-command 'ip -br link' | awk '/52:54:00:30:00:04/{print $1}' | sed 's/@.*//')
echo "transit interface: $tif"
[[ -n $tif ]] || { echo "no transit NIC"; exit 1; }
pgrep -f "fault-image-server.py" >/dev/null || setsid nohup python3 /work/field/fault-image-server.py /work/field/images > /work/field/fault-image-server.log 2>&1 < /dev/null &
for row in R9 R8 R3 R2; do
 case $row in R2) script=R2-vyos-upgrade.py;; *) script=$row-vyos.py;; esac
 printf '#!/bin/bash\nsource /work/field/lib.sh\nexport VYOS_TRANSIT_IF=%s\nshow "python3 /work/field/%s"\n' "$tif" "$script" > scene-t-$row-vyos-run.sh
 DEFER_RENDER=1 bash capture.sh t-$row-vyos-run scene-t-$row-vyos-run.sh > raw/$row-vyos-run.capture.log 2>&1
 echo "$row exit $(cat casts/t-$row-vyos-run.exit 2>/dev/null)"
done
echo vyos-block-done
