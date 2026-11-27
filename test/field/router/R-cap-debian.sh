#!/bin/bash
source /work/field/lib.sh
show 'date -u'
show '/work/field/guest-command debian "uname -r; dpkg-query -W frr nftables dnsmasq-base iproute2; command -v keepalived || true; command -v conntrackd || true; command -v wg || true; command -v ipsec || true; command -v pppd || true"'
show '/work/field/guest-command debian "ip link add field-cap-wg type wireguard; ip -d link show field-cap-wg; ip link del field-cap-wg"'
show '/work/field/guest-command debian "ip xfrm state list; dnsmasq --version; printf '\''enable-ra\ndhcp-range=fd42:ca::10,fd42:ca::20,64,12h\n'\'' >/root/field-cap-dnsmasq.conf; dnsmasq --test --conf-file=/root/field-cap-dnsmasq.conf"'
show '/work/field/guest-command debian "dpkg-query -W keepalived conntrackd wireguard-tools strongswan ppp wide-dhcpv6-client 2>&1 || true"'
