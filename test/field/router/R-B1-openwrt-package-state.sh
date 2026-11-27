#!/bin/bash
source /work/field/lib.sh
show 'date -u +%FT%TZ'
show "python3 /work/field/console.py openwrt 'cat /etc/openwrt_release'"
show "python3 /work/field/console.py openwrt 'wc -c /usr/lib/opkg/status /rom/usr/lib/opkg/status; ls -l /usr/sbin/bird'"
show "python3 /work/field/console.py openwrt 'cat /proc/meminfo | head -6; df -k /overlay'"
show "python3 /work/field/console.py openwrt 'dnsmasq --version | head -1; uptime'"
show 'date -u +%FT%TZ'
