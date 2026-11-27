#!/bin/bash
set -euo pipefail
export PATH=/work/toolchain/go/bin:$PATH TMPDIR=/work/build-main/tmp
export GOCACHE=/work/build-main/gocache GOPATH=/work/build-main/gopath GOMAXPROCS=1 GOFLAGS='-p=2 -buildvcs=false'
export KUBEROOT_OUT=/work/field7/out KUBEROOT_KEYS=/work/build-main/keys
src=/work/field7; out=$KUBEROOT_OUT/amd64; root=$src/root
mkdir -p "$out"
cd "$src"
for cmd in kinit kuberoot-node kuberoot-router; do
 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$out/$cmd" "./cmd/$cmd"
done
[[ -d $root ]] || cp -al /work/build-main/root "$root"
for cmd in kuberoot-node kuberoot-router; do cp --remove-destination "$out/$cmd" "$root/usr/bin/$cmd"; done
cp --remove-destination "$out/kinit" "$root/usr/sbin/kinit"
cp --remove-destination distros/router/addons/*.yaml "$root/usr/share/kuberoot/addons/"
cp --remove-destination distros/router/profile.yaml "$root/usr/share/kuberoot/profile.yaml"
version=${FIELD_VERSION:-0.1.1-router.field.3}
rm "$root/etc/os-release"
printf 'NAME="kuberoot"\nID=kuberoot\nVERSION_ID=%s\n' "$version" > "$root/etc/os-release"
printf '%s\n' "$version" > "$out/VERSION"
cp /work/build-main/out/amd64/vmlinuz.efi "$out/"
mksquashfs "$root" "$out/rootfs.squashfs" -noappend -comp zstd -processors 1
scripts/build-initramfs.sh amd64
scripts/build-bundle.sh amd64
cp "$out/bundles/"* /work/www/
sha256sum "$out/bundles/"*.tar
