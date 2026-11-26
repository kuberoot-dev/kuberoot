#!/bin/bash
# Run inside the Debian competitor, preparatory user-net profile only.
set -euo pipefail
mkdir -p /etc/systemd/network
cat > /etc/systemd/network/20-field-lan.network <<'NET'
[Match]
Name=enp0s3
[Network]
Address=192.168.30.1/24
NET
ip address replace 192.168.30.1/24 dev enp0s3
ip link set enp0s3 up
sysctl -w net.ipv4.ip_forward=1
cat > /etc/dnsmasq.d/field.conf <<'CONF'
interface=enp0s3
bind-interfaces
dhcp-range=192.168.30.100,192.168.30.199,255.255.255.0,1h
dhcp-option=3,192.168.30.1
dhcp-option=6,192.168.30.1
server=10.0.2.3
CONF
for table in 'ip fieldnat' 'inet fieldfilter'; do
 if nft list table $table >/dev/null 2>&1; then nft delete table $table; fi
done
nft -f - <<'NFT' 
table ip fieldnat {
 chain postrouting { type nat hook postrouting priority srcnat; policy accept;
  oifname "enp0s2" ip saddr 192.168.30.0/24 masquerade
 }
}
table inet fieldfilter {
 chain forward { type filter hook forward priority filter; policy drop;
  ct state established,related accept
  iifname "enp0s3" oifname "enp0s2" accept
 }
}
NFT
nft list ruleset > /etc/nftables.conf
sed -i 's/^bgpd=no/bgpd=yes/' /etc/frr/daemons
systemctl restart dnsmasq frr
systemctl enable nftables
