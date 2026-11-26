#!/bin/bash
# SSH to a mutable Linux DUT through the gateway, without a second DUT NIC.
set -euo pipefail
d=/work/field-edge/gateway
config=/work/field-edge/ssh-config
if ! test -f "$config"; then
 cat > "$config" <<CONFIG
Host field-gateway
 HostName 127.0.0.1
 Port 2222
 User observer
 IdentityFile $d/key
Host field-dut
 HostName 10.78.0.11
 User observer
 IdentityFile $d/key
 ProxyJump field-gateway
Host *
 BatchMode yes
 ConnectTimeout 5
 StrictHostKeyChecking accept-new
 UserKnownHostsFile /work/field-edge/dut-known-hosts
CONFIG
 chmod 600 "$config"
fi
exec ssh -F "$config" field-dut "$@"
