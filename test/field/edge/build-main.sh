#!/bin/bash
# Run only in the owned stand pod, after baseline capture finishes.
# Reuse unchanged published kernel/userland; rebuild every kuberoot command.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
require_stand
B=$FIELD_ROOT/build-main
S=$B/src
export TMPDIR=/work/tmp GOMAXPROCS=2 GOFLAGS='-p=2 -buildvcs=false'
export GOCACHE=$B/gocache GOMODCACHE=$B/gomod PATH=$B/go/bin:/work/bin:$PATH
mkdir -p "$B" "$B/out/amd64"
test "$(cat "$S/SOURCE_COMMIT")" = 1b29543967ae6fce971f0bf818000a6b04374b52
if ! test -x "$B/go/bin/go"; then
 curl -fL --retry 3 -o "$B/go-downloads.json" 'https://go.dev/dl/?mode=json&include=all'
 python3 - "$B/go-downloads.json" > "$B/go.sha256" <<'PY'
import json,sys
for release in json.load(open(sys.argv[1])):
 for f in release['files']:
  if f['filename']=='go1.26.5.linux-amd64.tar.gz':
   print(f['sha256']+'  go.tar.gz'); sys.exit(0)
raise SystemExit('Pinned Go compiler missing from official manifest')
PY
 curl -fL --retry 3 -o "$B/go.tar.gz" https://go.dev/dl/go1.26.5.linux-amd64.tar.gz
 (cd "$B"; sha256sum -c go.sha256; tar -xzf go.tar.gz)
fi
cd "$S"
O=$B/out/amd64
for cmd in kinit kuberoot-node kuberoot-installer kuberoot-fleet kuberoot-intents kuberoot-router kuberoot-vmctl kuberoot-aictl kuberoot-agent kuberoot-devices kuberoot-wsctl; do
 GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$O/$cmd" "./cmd/$cmd"
done
# The exact kubepkg pin is already built and validated in Phase 1.
grep -q 'v1.3.2-0.20261126003637-22170c1213a4' build/kubepkg/go.mod
unsquashfs -processors 2 -d "$B/rootfs" /work/series/0.1.0-capture.fix/rootfs.squashfs > "$B/unpack.log"
for binary in "$O"/kuberoot-*; do cp "$binary" "$B/rootfs/usr/bin/"; done
cp "$O/kinit" "$B/rootfs/usr/sbin/kinit"
# Keep the existing test-series signing key, so fault bundles share trust.
v=0.1.0-field-main.1b29543
printf '%s\n' "$v" > "$O/VERSION"
printf 'NAME="kuberoot"\nID=kuberoot\nPRETTY_NAME="kuberoot %s (edge)"\nVERSION_ID=%s\n' "$v" "$v" > "$B/rootfs/etc/os-release"
cp -a distros/edge/addons/. "$B/rootfs/usr/share/kuberoot/addons/"
mksquashfs "$B/rootfs" "$O/rootfs.squashfs" -noappend -no-xattrs -comp zstd -processors 2 -mem 256M -quiet
for f in vmlinuz.efi systemd-boot.efi; do cp /work/artifacts/$f "$O/$f"; done
KUBEROOT_OUT="$B/out" scripts/build-initramfs.sh amd64
KUBEROOT_OUT="$B/out" KUBEROOT_KEYS=/work/fix-build/keys scripts/build-bundle.sh amd64
sha256sum "$O"/rootfs.squashfs "$O"/initrd.cpio "$O"/vmlinuz.efi > "$B/image-sha256.txt"
