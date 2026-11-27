#!/bin/sh
set -eu
uci set network.lan.device=eth1
uci set network.lan.ipaddr=192.168.30.1
uci set network.wan.device=eth0
uci set network.wan.proto=dhcp
uci set dhcp.lan.start=100
uci set dhcp.lan.limit=100
uci commit network
uci commit dhcp
/etc/init.d/network restart
/etc/init.d/dnsmasq restart
/etc/init.d/firewall restart
