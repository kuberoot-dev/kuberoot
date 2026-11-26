#!/bin/bash
set -euo pipefail
port=${GATEWAY_SSH_PORT:-2222}
[[ $port =~ ^[0-9]+$ ]] && ((port>0 && port<65536)) || exit 2
exec ssh -o BatchMode=yes -o ConnectTimeout=5 -o StrictHostKeyChecking=accept-new -o "UserKnownHostsFile=${GATEWAY_KNOWN_HOSTS:-/work/field-edge/gateway/known_hosts}" -i /work/field-edge/gateway/key -p "$port" observer@127.0.0.1 "$@"
