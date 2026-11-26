#!/bin/bash
# Rebuild project binaries from pinned main; reuse capture.5 external rootfs payload.
# Run only in kr-router-stand, with source archive and signing key pre-staged.
set -euo pipefail
export PATH=/work/toolchain/go/bin:$PATH TMPDIR=/work/build-main/tmp
export GOCACHE=/work/build-main/gocache GOPATH=/work/build-main/gopath
export GOMAXPROCS=2 GOFLAGS='-p=2 -buildvcs=false'
export WORK=/work/build-main KUBEROOT_OUT=/work/build-main/out KUBEROOT_KEYS=/work/build-main/keys
src=$WORK/src; out=$KUBEROOT_OUT/amd64; root=$WORK/root
mkdir -p "$out" "$TMPDIR"
cd "$src"
for cmd in kinit kuberoot-node kuberoot-installer kuberoot-intents kuberoot-router kuberoot-vmctl kuberoot-aictl kuberoot-agent kuberoot-devices kuberoot-wsctl kuberoot-fleet; do
 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$out/$cmd" "./cmd/$cmd"
done
scripts/build-kubepkg.sh amd64
unsquashfs -processors 2 -d "$root" "$WORK/base/rootfs.squashfs"
for cmd in kuberoot-node kuberoot-installer kuberoot-intents kuberoot-router kuberoot-vmctl kuberoot-aictl kuberoot-agent kuberoot-devices kuberoot-wsctl kuberoot-fleet kubepkg kubepkg-operator; do
 install -m 755 "$out/$cmd" "$root/usr/bin/$cmd"
done
cp "$out"/kubepkg-crds/*.yaml "$root/usr/share/kuberoot/kubepkg/crds/"
cp -r "$src/rootfs/." "$root/"
cp "$src/distros/router/profile.yaml" "$root/usr/share/kuberoot/profile.yaml"
cp "$src/distros/router/addons/"*.yaml "$root/usr/share/kuberoot/addons/"
version=0.1.1-router.field.1
printf 'NAME="kuberoot"\nID=kuberoot\nPRETTY_NAME="kuberoot %s (router)"\nVERSION_ID=%s\nHOME_URL="https://github.com/kuberoot-dev/kuberoot"\n' "$version" "$version" > "$root/etc/os-release"
printf '%s\n' "$version" > "$out/VERSION"
cp -L "$WORK/base/vmlinuz.efi" "$out/vmlinuz.efi"
mksquashfs "$root" "$out/rootfs.squashfs" -noappend -comp zstd -processors 2
scripts/build-initramfs.sh amd64
scripts/build-bundle.sh amd64
cp "$out/bundles/kuberoot-$version-amd64.tar"* /work/www/
printf '1b29543967ae6fce971f0bf818000a6b04374b52\n' > "$out/SOURCE_COMMIT"
sha256sum "$out"/bundles/*.tar
