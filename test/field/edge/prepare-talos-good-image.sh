#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
require_stand
D=$FIELD_ROOT/talos-good
mkdir -p "$D"
python3 "$FIELD_SCRIPTS/oci-image.py" fetch ghcr.io/siderolabs/installer:v1.14.7 "$D/oci" > "$D/pin.json"
endpoint=$(cat "$FIELD_ROOT/oci-registry/endpoint")
skopeo copy --preserve-digests --dest-cert-dir "$FIELD_ROOT/registry-tls" "oci:$D/oci" "docker://$endpoint/field-talos/good:trial" > "$D/import.log"
skopeo inspect --raw --tls-verify=true --cert-dir "$FIELD_ROOT/registry-tls" "docker://$endpoint/field-talos/good:trial" > "$D/manifest.json"
python3 - "$D" "$endpoint" <<'PY'
import hashlib,json,pathlib,sys
p=pathlib.Path(sys.argv[1]);manifest=(p/'manifest.json').read_bytes();j=json.loads(manifest);assert 'layers' in j
r={'reference':sys.argv[2]+'/field-talos/good@sha256:'+hashlib.sha256(manifest).hexdigest(),'compressed_layer_bytes':sum(x['size'] for x in j['layers']),'layers':j['layers'],'scope':'official unchanged v1.14.7 installer; same-version reinstall with newly selected UKI'}
(p/'image.json').write_text(json.dumps(r,indent=2));print(json.dumps(r))
PY
