#!/bin/bash
# Read-only diagnosis of the observed offline boot stall. Never changes NTP.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
export KUBECONFIG=$FIELD_ROOT/talos/kubeconfig
G=$FIELD_SCRIPTS/gateway-command.sh
T='/home/observer/talosctl --talosconfig /home/observer/talosconfig --nodes 10.78.0.11 --endpoints 10.78.0.11'
show export "KUBECONFIG=$KUBECONFIG"
show date -u
show "$G" "$T services"
show "$G" "$T get timestatus -o yaml"
if show kubectl --request-timeout=5s get --raw /readyz; then :; fi
show date -u
