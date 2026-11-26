#!/bin/bash
# Configure only the dedicated Linux endpoint VMs over their observer SSH NIC.
set -euo pipefail
name=${1:?client or gateway}
case $name in client) port=2231; address=192.168.10.2/24; direct=198.18.33.1/24;; gateway) port=2232; address=198.18.0.2/24; direct=198.18.33.2/24;; *) exit 2;; esac
ssh -i /work/field/private/id_ed25519 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p "$port" root@127.0.0.1 bash -s -- "$name" "$address" "$direct" <<'GUEST'
set -euo pipefail
hostname "$1"
systemctl disable --now dnsmasq frr nftables || true
nft flush ruleset
ip address flush dev enp0s3
ip address add "$2" dev enp0s3
ip link set enp0s3 up
ip address replace "$3" dev enp0s4
ip link set enp0s4 up
sysctl -w net.ipv4.ip_forward=1
if [[ $1 == client ]]; then
 ip route replace 198.18.0.0/24 via 192.168.10.1
 ip route replace 198.19.0.1/32 via 192.168.10.1
else
 ip address replace 198.19.0.1/32 dev lo
 ip route replace 192.168.10.0/24 via 198.18.0.1
 iperf3 -s -D
fi
ip -br addr
ip route
GUEST
