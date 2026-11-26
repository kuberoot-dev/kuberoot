#!/bin/bash
# Changes only the dedicated Linux gateway guest, never the workshop namespace.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
G=$FIELD_SCRIPTS/gateway-command.sh
show date -u
show "$G" 'hostname; /usr/sbin/sysctl net.netfilter.nf_conntrack_count net.netfilter.nf_conntrack_max; sudo dmesg --time-format iso | grep "table full" | tail -n 8'
show "$G" 'printf "net.netfilter.nf_conntrack_max=65536\n" | sudo tee /etc/sysctl.d/91-field-conntrack.conf; sudo sysctl -p /etc/sysctl.d/91-field-conntrack.conf; /usr/sbin/sysctl net.netfilter.nf_conntrack_count net.netfilter.nf_conntrack_max'
show date -u
