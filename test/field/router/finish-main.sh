#!/bin/bash
# Finish the first incremental build with the freshly pinned kubepkg CRDs.
set -euo pipefail
export PATH=/work/toolchain/go/bin:$PATH TMPDIR=/work/build-main/tmp
export GOCACHE=/work/build-main/gocache GOPATH=/work/build-main/gopath GOMAXPROCS=2 GOFLAGS='-p=2 -buildvcs=false'
export WORK=/work/build-main KUBEROOT_OUT=/work/build-main/out KUBEROOT_KEYS=/work/build-main/keys
for i in $(seq 1 240); do [[ -f $WORK/build.exit ]] && break; sleep 5; done
[[ $(cat "$WORK/build.exit") == 0 ]]
out=$KUBEROOT_OUT/amd64; root=$WORK/root; src=$WORK/src
cp "$out"/kubepkg-crds/*.yaml "$root/usr/share/kuberoot/kubepkg/crds/"
cp -r "$src/rootfs/." "$root/"
cp "$src/distros/router/profile.yaml" "$root/usr/share/kuberoot/profile.yaml"
cp "$src/distros/router/addons/"*.yaml "$root/usr/share/kuberoot/addons/"
mksquashfs "$root" "$out/rootfs.squashfs" -noappend -comp zstd -processors 2
cd "$src"
scripts/build-initramfs.sh amd64
scripts/build-bundle.sh amd64
cp "$out"/bundles/kuberoot-0.1.1-router.field.1-amd64.tar* /work/www/
python3 - <<'PY'
from pathlib import Path
import json,hashlib
p=Path('/work/build-main/out/amd64')
manifest={'source_commit':'1b29543967ae6fce971f0bf818000a6b04374b52','version':(p/'VERSION').read_text().strip(),'external_payload_base_bundle_sha256':'7550dafa853468e258216625453b8e462d7ec5104cf46320a34fba59d2e2eb42','method':'All project binaries and pinned kubepkg rebuilt; kernel and external rootfs packages reused from capture.5. Rootfs overlays/profile/addons and kubepkg CRDs copied from pinned source/build.','files':{str(x.relative_to(p)):hashlib.file_digest(x.open('rb'),'sha256').hexdigest() for x in p.glob('bundles/*.tar')}}
(p/'build-manifest.json').write_text(json.dumps(manifest,indent=2)+'\n')
print(json.dumps(manifest))
PY
