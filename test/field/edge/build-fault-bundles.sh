#!/bin/bash
# Build only between measured runs. Test-only bundles keep the base kernel/initrd
# and change the root profile plus a deliberately faulty guest service.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
require_stand
base=${1:?base artifact directory}
lane=${2:?unique build lane label}
[[ $lane =~ ^[A-Za-z0-9_.-]+$ ]] || exit 2
B=$FIELD_ROOT/fault-build-$lane
mkdir "$B"
export PATH=$FIELD_ROOT/build-main/go/bin:/work/bin:$PATH GOMAXPROCS=2 GOFLAGS='-p=2 -buildvcs=false'
export GOCACHE=$B/gocache GOMODCACHE=$FIELD_ROOT/build-main/gomod CGO_ENABLED=0
source_tree=$FIELD_ROOT/build-main/src
(cd "$source_tree"; go build -trimpath -ldflags='-s -w' -o "$B/kuberoot-release" ./cmd/kuberoot-release)
go build -trimpath -ldflags='-s -w' -o "$B/fixture-agent" "$FIELD_SCRIPTS/fixture-agent.go"
unsquashfs -processors 2 -d "$B/root" "$base/rootfs.squashfs" > "$B/unpack.log"
cp "$B/root/usr/share/kuberoot/profile.yaml" "$B/original-profile.yaml"
install -m 755 "$B/fixture-agent" "$B/root/usr/bin/field-fault-agent"
# Matching the baked public key is mandatory; signature verification stays on.
cmp "$B/root/usr/share/kuberoot/release.pub" /work/fix-build/keys/release.pub
mkdir -p "$FIELD_ROOT/www" "$B/payload"
for f in vmlinuz.efi initrd.cpio; do cp "$base/$f" "$B/payload/$f"; done
for mode in init-crash pod-network wrong-content incompatible-state; do
 version=0.1.0-field-$lane-$mode
 python3 - "$B/original-profile.yaml" "$B/root/usr/share/kuberoot/profile.yaml" "$mode" <<'PY'
import pathlib,sys
s=pathlib.Path(sys.argv[1]).read_text();needle='      services:\n';assert needle in s
fault='      - name: field-test-fault\n        args: [/usr/bin/field-fault-agent, '+sys.argv[3]+']\n'
pathlib.Path(sys.argv[2]).write_text(s.replace(needle,needle+fault,1))
PY
 printf 'NAME="kuberoot"\nID=kuberoot\nPRETTY_NAME="kuberoot %s TEST FAULT"\nVERSION_ID=%s\n' "$version" "$version" > "$B/root/etc/os-release"
 printf '%s\n' "$version" > "$B/payload/VERSION"
 mksquashfs "$B/root" "$B/payload/rootfs.squashfs" -noappend -no-xattrs -comp zstd -processors 2 -mem 256M -quiet
 bundle=$FIELD_ROOT/www/$version.tar
 tar -C "$B/payload" -cf "$bundle" VERSION vmlinuz.efi initrd.cpio rootfs.squashfs
 "$B/kuberoot-release" sign "$bundle" /work/fix-build/keys/release.key
 sha256sum "$bundle" > "$bundle.sha256"
 sha256sum "$B/payload/rootfs.squashfs" > "$B/$mode-root.sha256"
 # Keep signed bundles and hashes; intermediate root images are reproducible.
 rm -- "$B/payload/rootfs.squashfs"
done
rm -rf "$B/root" "$B/gocache"
printf 'TEST-ONLY bundles built from %s; no product run is implied.\n' "$base"
