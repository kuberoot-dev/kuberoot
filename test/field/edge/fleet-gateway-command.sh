#!/bin/bash
# Commands run inside one of the three owned Linux NAT VMs, never the pod host.
set -euo pipefail
id=${1:?1..3};shift
[[ $id =~ ^[123]$ ]] || exit 2
exec ssh -o BatchMode=yes -o ConnectTimeout=5 -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/work/field-edge/fleet-field/gateway-known-hosts -i /work/field-edge/gateway/key -p "2230$id" observer@127.0.0.1 "$@"
