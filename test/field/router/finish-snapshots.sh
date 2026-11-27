#!/bin/bash
# Run after the VyOS installed snapshot finishes. No benchmark verdicts here.
set -euo pipefail
python3 /work/field/qmp.py vyos quit
bash /work/field/competitor-vm.sh openwrt
python3 /work/field/console.py openwrt 'cat /proc/uptime' --timeout 180 > /work/field/raw/openwrt-ready.txt
bash /work/field/capture.sh t-R-B1-openwrt-memory /work/field/R-B1-openwrt.sh > /work/field/raw/capture-openwrt.log 2>&1
python3 /work/field/qmp.py openwrt quit
bash /work/field/competitor-vm.sh debian
ready=0
for attempt in $(seq 1 60); do
 if ssh -F /work/field/private/ssh.conf -o ConnectTimeout=2 debian-field true; then ready=1; break; fi
 sleep 2
done
[[ $ready == 1 ]]
bash /work/field/capture.sh t-R-B1-debian-memory /work/field/R-B1-debian.sh > /work/field/raw/capture-debian.log 2>&1
date -u +%FT%TZ > /work/field/raw/snapshots-completed.txt
