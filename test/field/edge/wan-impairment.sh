#!/bin/bash
# Controls only gateway guest interfaces. Local LAN probes remain unimpaired.
set -euo pipefail
G=$(dirname "$0")/gateway-command.sh
if [[ ${1:-} == lossy || ${1:-} == slow ]]; then
 "$G" 'sudo modprobe ifb; sudo ip link add ifbwan type ifb 2>/dev/null || true; sudo ip link set ifbwan up; sudo tc qdisc replace dev wan0 handle ffff: ingress; sudo tc filter replace dev wan0 parent ffff: protocol all u32 match u32 0 0 action mirred egress redirect dev ifbwan'
fi
# Reset only this fixture's own rules, not unrelated guest networking.
"$G" 'sudo nft flush chain inet impairment forward; sudo nft delete chain inet impairment dns_output 2>/dev/null || true'
case "${1:?clean|blackhole|lossy|slow}" in
 clean) "$G" 'sudo tc qdisc del dev wan0 root 2>/dev/null || true; sudo tc qdisc del dev ifbwan root 2>/dev/null || true' ;;
 blackhole) "$G" 'sudo nft add rule inet impairment forward drop; sudo nft add chain inet impairment dns_output "{ type filter hook output priority filter; policy accept; }"; sudo nft add rule inet impairment dns_output oifname "wan0" udp dport 53 drop; sudo nft add rule inet impairment dns_output oifname "wan0" tcp dport 53 drop' ;;
 lossy) "$G" 'sudo tc qdisc replace dev wan0 root netem delay 100ms 20ms loss 1%; sudo tc qdisc replace dev ifbwan root netem delay 100ms 20ms loss 1%' ;;
 slow) "$G" 'sudo tc qdisc replace dev wan0 root tbf rate 2mbit burst 32kbit latency 400ms; sudo tc qdisc replace dev ifbwan root tbf rate 2mbit burst 32kbit latency 400ms' ;;
 *) exit 2 ;;
esac
"$G" 'date -u; sudo nft list table inet impairment; sudo tc -s qdisc show dev wan0; sudo tc -s qdisc show dev ifbwan 2>/dev/null || true'
