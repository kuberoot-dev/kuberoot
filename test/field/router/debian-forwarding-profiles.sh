#!/bin/bash
set -euo pipefail
# All endpoint iperf servers and the four-VM fabric already exist.
for profile in routing nat stateful; do
 if [[ $profile == routing ]]; then
  /work/field/guest-command debian 'nft flush ruleset'
 elif [[ $profile == nat ]]; then
  /work/field/guest-command debian 'nft -f /etc/nftables.conf; nft delete table inet fieldfilter'
 else
  /work/field/guest-command debian 'nft flush ruleset; nft -f /etc/nftables.conf'
 fi
 /work/field/guest-command debian 'date -u; nft list ruleset; ip -br addr; ethtool -k enp0s2; ethtool -l enp0s2' > "/work/field/raw/R-B2-debian-$profile-state.txt" 2>&1 || true
 python3 /work/field/forwarding.py debian "$profile"
done
