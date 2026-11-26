#!/bin/bash
# Sign a real installer with an explicitly foreign key; product-default policy
# remains unchanged. Corrupt an actual blob on a separate TLS registry endpoint.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
D=$FIELD_ROOT/oci-registry
TLS=$FIELD_ROOT/registry-tls
endpoint=$(cat "$D/endpoint")
mkdir "$D/foreign-signer"
export COSIGN_PASSWORD=
(cd "$D/foreign-signer"; "$D/cosign" generate-key-pair > keygen.log 2>&1)
skopeo copy --preserve-digests --src-cert-dir "$TLS" --dest-cert-dir "$TLS" "docker://$endpoint/field-talos/wrong-content:trial" "docker://$endpoint/field-talos/untrusted:trial" > "$D/import-untrusted.log"
# TLS verification stays enabled; this file augments system trust for cosign.
cat /etc/ssl/certs/ca-certificates.crt "$TLS/ca.crt" > "$D/ca-bundle.crt"
SSL_CERT_FILE=$D/ca-bundle.crt "$D/cosign" sign --yes --tlog-upload=false --key "$D/foreign-signer/cosign.key" "$endpoint/field-talos/untrusted:trial" > "$D/sign-untrusted.log" 2>&1
SSL_CERT_FILE=$D/ca-bundle.crt "$D/cosign" verify --key "$D/foreign-signer/cosign.pub" --insecure-ignore-tlog "$endpoint/field-talos/untrusted:trial" > "$D/foreign-signature-verification.json"
python3 - "$FIELD_ROOT" <<'PY'
import json,pathlib,sys
root=pathlib.Path(sys.argv[1]);layout=root/'talos-fault-build/wrong-content/installer-oci'
i=json.loads((layout/'index.json').read_text())['manifests'][0];m=json.loads((layout/'blobs/sha256'/i['digest'][7:]).read_text());blob=max(m['layers'],key=lambda x:x['size'])['digest']
(root/'oci-registry/tampered-catalog.json').write_text(json.dumps({'field-talos/tampered':{'layout':str(layout),'tag':'trial','corrupt_blob':blob}},indent=2))
PY
nohup python3 "$FIELD_SCRIPTS/oci-registry.py" --catalog "$D/tampered-catalog.json" --port 15006 --cert "$TLS/registry-server.crt" --key "$TLS/server.key" --log "$D/tampered-requests.jsonl" > "$D/tampered-server.log" 2>&1 </dev/null &
echo "$!" > "$D/tampered-server.pid"
sleep 1
curl --fail --silent --cacert "$TLS/ca.crt" "https://${endpoint%:*}:15006/v2/"
