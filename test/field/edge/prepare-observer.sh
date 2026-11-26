#!/bin/bash
# The driver runs on the gateway's LAN, outside the DUT, with a strict kubeconfig.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
: "${KUBECONFIG:?}"
config=$FIELD_ROOT/observer-kubeconfig
kubectl config view --raw -o json | python3 -c 'import json,sys; x=json.load(sys.stdin); [(c["cluster"].update({"server":"https://10.78.0.11:6443","tls-server-name":"10.78.0.11"})) for c in x["clusters"]]; assert not any(c["cluster"].get("insecure-skip-tls-verify") for c in x["clusters"]); print(json.dumps(x))' > "$config"
chmod 600 "$config"
scp -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o "UserKnownHostsFile=${GATEWAY_KNOWN_HOSTS:-$FIELD_ROOT/gateway/known_hosts}" \
 -i "$FIELD_ROOT/gateway/key" -P "${GATEWAY_SSH_PORT:-2222}" /work/bin/kubectl "$FIELD_SCRIPTS/observer.py" "$FIELD_SCRIPTS/upgrade_observations.py" observer@127.0.0.1:/home/observer/
scp -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o "UserKnownHostsFile=${GATEWAY_KNOWN_HOSTS:-$FIELD_ROOT/gateway/known_hosts}" \
 -i "$FIELD_ROOT/gateway/key" -P "${GATEWAY_SSH_PORT:-2222}" "$config" observer@127.0.0.1:/home/observer/kubeconfig
"$FIELD_SCRIPTS/gateway-command.sh" 'sudo install -m 755 /home/observer/kubectl /usr/local/bin/kubectl; chmod 600 /home/observer/kubeconfig'
if [[ ${1:-} != --config-only ]]; then
 "$FIELD_SCRIPTS/gateway-command.sh" 'KUBECONFIG=/home/observer/kubeconfig kubectl get nodes; curl --fail --max-time 5 http://10.78.0.11:30080/http | wc -c'
fi
