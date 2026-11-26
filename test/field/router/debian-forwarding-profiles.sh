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
 cat > "/work/field/R-B2-debian-$profile.sh" <<SCENE
#!/bin/bash
source /work/field/lib.sh
show 'date -u +%FT%TZ'
show 'python3 /work/field/forwarding.py debian $profile --keep-going'
show 'date -u +%FT%TZ'
SCENE
 DEFER_RENDER=1 bash /work/field/capture.sh "t-R-B2-debian-$profile" "/work/field/R-B2-debian-$profile.sh"
done
