#!/bin/bash
# Build the application-health fix only after original E2 c/d findings exist,
# and only between measured runs. Existing source checkout/images stay intact.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
require_stand
B=$FIELD_ROOT/build-health
S=$FIELD_ROOT/health-gate-draft/src
commit=52db7b1fba8dc386e519cd137d6bf42afce6c756
python3 - "$FIELD_ROOT" <<'PY'
import json,pathlib,sys
root=pathlib.Path(sys.argv[1])
for path in pathlib.Path('/proc').glob('[0-9]*/cmdline'):
 try:args=path.read_bytes().decode().split('\0')
 except (OSError,UnicodeError):continue
 if args[0].endswith('qemu-system-x86_64'):
  assert '-name' in args and args[args.index('-name')+1]=='edge-field-gateway','Stop measured DUTs before building'
rows=[json.loads(x) for x in (root/'results.jsonl').read_text().splitlines()]
for phase in ['wrong-content','incompatible-state']:
 assert any(r.get('scenario_id')=='E2' and r.get('product')=='kuberoot-main-1b29543' and r.get('fault_phase')==phase and r.get('status')=='failed' and r.get('metrics',{}).get('faulty_release_marked_good') for r in rows),'Record original E2 finding first: '+phase
assert json.loads((root/'health-gate-draft/check-result.json').read_text())['exit_code']==0
PY
test ! -e "$B" || { printf 'Review partial health build before restarting: %s\n' "$B" >&2;exit 2; }
(cd "$S";sha256sum -c "$FIELD_ROOT/health-gate-draft/source.sha256")
mkdir -p "$B/out/amd64"
printf '%s\n' "$commit" > "$B/SOURCE_COMMIT"
export TMPDIR=/work/tmp GOMAXPROCS=2 GOMEMLIMIT=2GiB GOFLAGS='-p=2 -buildvcs=false' CGO_ENABLED=0
export GOCACHE=$FIELD_ROOT/build-main/gocache GOMODCACHE=$FIELD_ROOT/build-main/gomod PATH=$FIELD_ROOT/build-main/go/bin:/work/bin:$PATH
cd "$S"
O=$B/out/amd64
for cmd in kinit kuberoot-node kuberoot-installer kuberoot-fleet kuberoot-intents kuberoot-router kuberoot-vmctl kuberoot-aictl kuberoot-agent kuberoot-devices kuberoot-wsctl;do
 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o "$O/$cmd" "./cmd/$cmd"
done
unsquashfs -processors 2 -d "$B/rootfs" "$FIELD_ROOT/build-main/out/amd64/rootfs.squashfs" > "$B/unpack.log"
for binary in "$O"/kuberoot-*;do cp "$binary" "$B/rootfs/usr/bin/";done
cp "$O/kinit" "$B/rootfs/usr/sbin/kinit"
cmp "$B/rootfs/usr/share/kuberoot/release.pub" /work/fix-build/keys/release.pub
v=0.1.0-field-health.52db7b1
printf '%s\n' "$v" > "$O/VERSION"
printf 'NAME="kuberoot"\nID=kuberoot\nPRETTY_NAME="kuberoot %s (edge health gate)"\nVERSION_ID=%s\n' "$v" "$v" > "$B/rootfs/etc/os-release"
mksquashfs "$B/rootfs" "$O/rootfs.squashfs" -noappend -no-xattrs -comp zstd -processors 2 -mem 256M -quiet
for file in vmlinuz.efi systemd-boot.efi;do cp "$FIELD_ROOT/build-main/out/amd64/$file" "$O/$file";done
KUBEROOT_OUT="$B/out" scripts/build-initramfs.sh amd64
KUBEROOT_OUT="$B/out" KUBEROOT_KEYS=/work/fix-build/keys scripts/build-bundle.sh amd64
sha256sum "$O/rootfs.squashfs" "$O/initrd.cpio" "$O/vmlinuz.efi" > "$B/image-sha256.txt"
# Only intermediate expansion/combined initramfs are removed. All signed UEFI
# boot payloads, source, binaries and provenance stay available for reproduction.
rm -rf -- "$B/rootfs"
rm -- "$O/initramfs.cpio"
date -u > "$B/built"
