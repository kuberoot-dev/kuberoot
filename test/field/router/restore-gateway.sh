#!/bin/bash
# Re-create the gateway's runtime-only state: R1 management namespace and proxy,
# R8 observer namespace, transit addresses, and the pod tunnel to the proxy.
set -euo pipefail
g() { /work/field/guest-command gateway "$1"; }
until g true 2>/dev/null; do sleep 3; done
g 'ip addr replace 10.255.0.3/24 dev enp0s6; ip addr add 10.255.0.4/24 dev enp0s6 2>/dev/null || true; ip link set enp0s6 up; sysctl -qw net.ipv4.ip_forward=1
ip netns add fieldmanagement 2>/dev/null || true
ip link show fmhost >/dev/null 2>&1 || { ip link add fmhost type veth peer name fmguest; ip link set fmguest netns fieldmanagement; }
ip addr replace 172.31.250.1/24 dev fmhost; ip link set fmhost up
ip netns exec fieldmanagement ip addr replace 172.31.250.2/24 dev fmguest; ip netns exec fieldmanagement ip link set fmguest up; ip netns exec fieldmanagement ip link set lo up
ip netns exec fieldmanagement ip route replace default via 172.31.250.1
pgrep -f TCP-LISTEN:16443 >/dev/null || systemd-run --unit field-proxy-16443 ip netns exec fieldmanagement socat TCP-LISTEN:16443,bind=172.31.250.2,reuseaddr,fork TCP:10.255.0.1:6443
ip netns add fieldobserver 2>/dev/null || true
ip netns exec fieldobserver ip link show fieldobserver >/dev/null 2>&1 || { ip link add link enp0s6 fieldobserver type macvlan mode bridge; ip link set fieldobserver netns fieldobserver; }
ip netns exec fieldobserver ip link set lo up; ip netns exec fieldobserver ip addr replace 10.255.0.5/24 dev fieldobserver; ip netns exec fieldobserver ip link set fieldobserver up
mkdir -p /run/field-r8 /run/field-bird; ip -br a'
pgrep -f "16443:172.31.250.2" >/dev/null || ssh -f -N -L 127.0.0.1:16443:172.31.250.2:16443 -o ExitOnForwardFailure=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -i /work/field/private/id_ed25519 -p 2232 root@127.0.0.1
