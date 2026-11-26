#!/bin/bash
# A valid transport hash must not substitute for a trusted release signature.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
require_stand
lane=${1:?lane}
[[ $lane =~ ^[A-Za-z0-9_.-]+$ ]]
base=$FIELD_ROOT/www/0.1.0-field-$lane-wrong-content.tar
out=$FIELD_ROOT/www/0.1.0-field-$lane
keydir=$FIELD_ROOT/fault-build-$lane/untrusted-key
mkdir -m 700 "$keydir"
/work/fix-build/bin/kuberoot-release keygen "$keydir/release"
# Same payload, signed by a key absent from the guest's trust store.
ln "$base" "$out-untrusted.tar"
/work/fix-build/bin/kuberoot-release sign "$out-untrusted.tar" "$keydir/release.key"
# A byte added after the tar end marker changes the digest but keeps the original
# signature. The request carries this new digest, so transport SHA verification
# succeeds and the cryptographic trust verifier must reject the substitution.
cp --reflink=auto "$base" "$out-tampered.tar"
printf '\001' >> "$out-tampered.tar"
cp "$base.sig" "$out-tampered.tar.sig"
sha256sum "$out-untrusted.tar" > "$out-untrusted.tar.sha256"
sha256sum "$out-tampered.tar" > "$out-tampered.tar.sha256"
python3 - "$base" "$out" "$keydir/release.pub" <<'PY'
import hashlib,json,pathlib,sys
base,out,key=sys.argv[1:]
def sha(p):
 h=hashlib.sha256()
 with open(p,'rb') as f:
  for b in iter(lambda:f.read(1024*1024),b''):h.update(b)
 return h.hexdigest()
r={'source_bundle_sha256':sha(base),'untrusted_bundle_sha256':sha(out+'-untrusted.tar'),'tampered_bundle_sha256':sha(out+'-tampered.tar'),'untrusted_public_key_sha256':sha(key),'tamper':'one byte appended after tar end; original signature retained; transport hash recomputed'}
assert r['source_bundle_sha256']==r['untrusted_bundle_sha256']
assert r['source_bundle_sha256']!=r['tampered_bundle_sha256']
pathlib.Path(out+'-negative-fixtures.json').write_text(json.dumps(r,indent=2))
print(json.dumps(r))
PY
