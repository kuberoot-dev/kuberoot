#!/bin/bash
source /work/field/lib.sh
show 'date -u +%FT%TZ'
show 'cat /work/field/private/ssh.conf'
show "ssh -F /work/field/private/ssh.conf debian-field 'cat /etc/debian_version; uname -r'"
show "ssh -F /work/field/private/ssh.conf debian-field 'dpkg-query -W frr nftables dnsmasq'"
show "ssh -F /work/field/private/ssh.conf debian-field 'free -b; df -B1 /; ip -4 route'"
show 'date -u +%FT%TZ'
