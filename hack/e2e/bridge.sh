#!/usr/bin/env bash
# The machines' shared network for hack/e2e/etcd-ha.sh: a bridge with a tap
# per node, owned by the user who runs QEMU. Run as root.
# Usage: hack/e2e/bridge.sh <user>
set -euo pipefail
user=${1:?user required}
ip link add e2ebr type bridge
ip link set e2ebr up
for i in 1 2 3; do
  ip tuntap add "e2etap$i" mode tap user "$user"
  ip link set "e2etap$i" master e2ebr up
done
# Docker's FORWARD policy would drop bridged frames that br_netfilter hands
# to iptables.
sysctl -q -w net.bridge.bridge-nf-call-iptables=0 net.bridge.bridge-nf-call-ip6tables=0 2>/dev/null || true
iptables -I FORWARD -i e2ebr -o e2ebr -j ACCEPT
