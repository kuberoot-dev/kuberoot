#!/bin/bash
# Run in the Debian DUT BEFORE switching QEMU from preparation to fabric.
set -euo pipefail
mkdir -p /etc/systemd/network
cat > /etc/systemd/network/05-field-wan.network <<'EOF'
[Match]
MACAddress=52:54:00:30:00:01
[Network]
Address=198.18.0.1/24
Gateway=198.18.0.2
DNS=1.1.1.1
EOF
cat > /etc/systemd/network/05-field-lan.network <<'EOF'
[Match]
MACAddress=52:54:00:30:00:02
[Network]
Address=192.168.10.1/24
EOF
cat > /etc/systemd/network/05-field-observer.network <<'EOF'
[Match]
MACAddress=52:54:00:30:00:03
[Network]
DHCP=ipv4
[DHCPv4]
UseRoutes=false
UseDNS=false
EOF
sed -i 's/192.168.30/192.168.10/g' /etc/dnsmasq.d/field.conf /etc/nftables.conf
sync
