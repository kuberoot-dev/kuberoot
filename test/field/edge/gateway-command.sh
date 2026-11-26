#!/bin/bash
set -euo pipefail
exec ssh -o BatchMode=yes -o ConnectTimeout=5 -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/work/field-edge/gateway/known_hosts -i /work/field-edge/gateway/key -p 2222 observer@127.0.0.1 "$@"
