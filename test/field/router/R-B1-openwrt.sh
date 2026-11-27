#!/bin/bash
source /work/field/lib.sh
show 'date -u +%FT%TZ'
show "python3 /work/field/console.py openwrt 'cat /etc/openwrt_release; cat /proc/meminfo | head -6; df -k /overlay'"
show "python3 /work/field/console.py openwrt 'ip -4 address show; ip -4 route show'"
show "python3 /work/field/console.py openwrt 'opkg list-installed | grep -e bird -e dnsmasq -e firewall4 -e rpcd -e kernel'"
show 'date -u +%FT%TZ'
show "python3 /work/field/console.py openwrt 'uname -r; uptime; cat /proc/meminfo | head -6'"
