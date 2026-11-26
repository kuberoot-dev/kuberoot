#!/bin/bash
# Real OCI Distribution registry for standard cosign signatures, no Docker.
# Run between product measurements, after Talos installer images are built.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
require_stand
D=$FIELD_ROOT/oci-registry
mkdir -p "$D" /work/apt/archives/partial /work/apt/lists/partial
python3 "$FIELD_SCRIPTS/oci-image.py" fetch registry-1.docker.io/library/registry:3.0.0 "$D/vendor-oci" > "$D/vendor-pin.json"
python3 "$FIELD_SCRIPTS/oci-image.py" extract-file "$D/vendor-oci" bin/registry "$D/registry"
sha256sum "$D/registry" > "$D/registry.sha256"
if ! command -v skopeo >/dev/null; then
 apt-get -o Dir::Cache::archives=/work/apt/archives -o Dir::State::lists=/work/apt/lists update
 apt-get -o Dir::Cache::archives=/work/apt/archives -o Dir::State::lists=/work/apt/lists install -y --no-install-recommends skopeo
fi
curl -fL --retry 3 -o "$D/cosign" https://github.com/sigstore/cosign/releases/download/v3.1.6/cosign-linux-amd64
printf '318a99ade4156a0c4113aa983b46c0a0830fa7896a096c63121ee47b610a94c6  %s\n' "$D/cosign" | sha256sum -c -
chmod 755 "$D/cosign"
# Use the owned pod IP so the pod and DUT address the same registry reference.
pod_ip=$(python3 -c 'import socket;print(socket.gethostbyname(socket.gethostname()))')
python3 - "$pod_ip" <<'PY'
import ipaddress,sys
assert ipaddress.ip_address(sys.argv[1]).version==4
PY
TLS=$FIELD_ROOT/registry-tls
cat > "$TLS/registry-server.ext" <<EXT
subjectAltName=IP:$pod_ip,IP:10.0.2.2,IP:127.0.0.1
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=serverAuth
EXT
openssl x509 -req -in "$TLS/server.csr" -CA "$TLS/ca.crt" -CAkey "$TLS/ca.key" -CAcreateserial -out "$TLS/registry-server.crt" -days 365 -extfile "$TLS/registry-server.ext"
cat > "$D/config.yaml" <<YAML
version: 0.1
storage:
  filesystem:
    rootdirectory: $D/data
http:
  addr: 0.0.0.0:15005
  tls:
    certificate: $TLS/registry-server.crt
    key: $TLS/server.key
YAML
nohup "$D/registry" serve "$D/config.yaml" > "$D/server.log" 2>&1 </dev/null &
echo "$!" > "$D/server.pid"
for i in $(seq 1 30); do
 if curl --fail --silent --cacert "$TLS/ca.crt" "https://$pod_ip:15005/v2/"; then break;fi
 sleep 1
done
curl --fail --silent --cacert "$TLS/ca.crt" "https://$pod_ip:15005/v2/" >/dev/null
printf '%s:15005\n' "$pod_ip" > "$D/endpoint"
python3 - "$pod_ip" "$TLS" "$D" <<'PY'
import base64,json,pathlib,sys
ip,tls,out=sys.argv[1:];tls=pathlib.Path(tls);out=pathlib.Path(out)
ca=base64.b64encode((tls/'ca.crt').read_bytes()).decode()
patch={'machine':{'registries':{'config':{f'{ip}:{port}':{'tls':{'ca':ca}} for port in [15005,15006]}}}}
(out/'talos-registry-patch.json').write_text(json.dumps(patch,indent=2))
PY
for mode in init-crash pod-network wrong-content incompatible-state; do
 skopeo copy --preserve-digests --dest-cert-dir "$TLS" "oci:$FIELD_ROOT/talos-fault-build/$mode/installer-oci" "docker://$pod_ip:15005/field-talos/$mode:trial" > "$D/import-$mode.log"
done
skopeo --version > "$D/tool-versions.txt"
"$D/cosign" version >> "$D/tool-versions.txt"
printf 'Registry and four real fault images ready; no Talos upgrade has been run.\n'
