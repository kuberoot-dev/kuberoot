#!/bin/bash
# Trust only the laboratory registry CA; retain all normal TLS verification.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
require_stand
D=$FIELD_ROOT/registry-tls
mkdir "$D"
umask 077
openssl req -x509 -newkey ed25519 -nodes -keyout "$D/ca.key" -out "$D/ca.crt" -days 1825 -subj /CN=field-edge-registry-test-ca -addext basicConstraints=critical,CA:TRUE -addext keyUsage=critical,keyCertSign,cRLSign > "$D/keygen.log" 2>&1
openssl req -new -newkey ed25519 -nodes -keyout "$D/server.key" -out "$D/server.csr" -subj /CN=field-edge-registry >> "$D/keygen.log" 2>&1
cat > "$D/server.ext" <<'EXT'
subjectAltName=IP:10.0.2.2,IP:127.0.0.1
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=serverAuth
EXT
openssl x509 -req -in "$D/server.csr" -CA "$D/ca.crt" -CAkey "$D/ca.key" -CAcreateserial -out "$D/server.crt" -days 365 -extfile "$D/server.ext" >> "$D/keygen.log" 2>&1
python3 - "$D" <<'PY'
import base64,json,pathlib,sys
p=pathlib.Path(sys.argv[1]);patch={'machine':{'registries':{'config':{'10.0.2.2:15005':{'tls':{'ca':base64.b64encode((p/'ca.crt').read_bytes()).decode()}}}}}}
(p/'talos-registry-patch.json').write_text(json.dumps(patch,indent=2))
PY
# Offline validation against the exact existing Talos config; no guest mutation.
base=${TALOS_BASE_CONFIG:-$FIELD_ROOT/talos/config/controlplane-fixed.yaml}
test -f "$base" || base=$FIELD_ROOT/talos/config/controlplane.yaml
talosctl validate --config "$base" --mode metal
talosctl machineconfig patch "$base" --patch @"$D/talos-registry-patch.json" --output "$D/talos-config.private.yaml"
talosctl validate --config "$D/talos-config.private.yaml" --mode metal
openssl verify -CAfile "$D/ca.crt" "$D/server.crt"
sha256sum "$D/ca.crt" "$D/server.crt" > "$D/public-cert-sha256.txt"
printf 'Registry TLS materials and offline Talos patch prepared; no guest config applied.\n'
