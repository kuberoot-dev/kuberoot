#!/bin/bash
source /work/field/lib.sh
show 'date -u +%FT%TZ'
show "python3 /work/field/console.py vyos 'show version'"
show "python3 /work/field/console.py vyos 'free -b; df -B1 /'"
show "python3 /work/field/console.py vyos 'uname -r; uptime; ip -4 route show'"
show 'date -u +%FT%TZ'
show "python3 /work/field/console.py vyos 'dpkg-query -W vyos-1x frr; cat /proc/meminfo | head -4'"
