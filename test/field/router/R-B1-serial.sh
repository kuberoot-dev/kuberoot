#!/bin/bash
source /work/field/lib.sh
# Only the explicitly named competitor is rebooted; no console recovery.
show 'date -u +%FT%TZ'
show 'python3 /work/field/qmp.py openwrt system_reset'
show 'timeout 40 tail -n 8 -f /work/vms/openwrt/console.log || test "$?" = 124'
show 'date -u +%FT%TZ'
show "python3 /work/field/console.py openwrt 'cat /proc/uptime; ip -4 address show eth0'"
