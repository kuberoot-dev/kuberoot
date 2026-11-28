#!/bin/bash
# Build the router image from main inside the stand pod: Go binaries on Debian,
# the root filesystem in an Alpine chroot (build-rootfs.sh needs apk), the
# kernel and systemd-boot from release v0.1.0 (kernel/ is unchanged since).
set -euo pipefail
export PATH=/work/toolchain/go/bin:$PATH TMPDIR=/work/tmp GOCACHE=/work/gocache GOPATH=/work/gopath GOMAXPROCS=4 GOFLAGS=-buildvcs=false
A=/work/alpine
if [ ! -x $A/sbin/apk ]; then
  mkdir -p $A; curl -fsSL https://dl-cdn.alpinelinux.org/alpine/v3.22/releases/x86_64/latest-releases.yaml > /work/tmp/alpine.yaml
  f=$(awk '/file: alpine-minirootfs/ {print $2; exit}' /work/tmp/alpine.yaml)
  curl -fsSL https://dl-cdn.alpinelinux.org/alpine/v3.22/releases/x86_64/$f | tar -xz -C $A
fi
cp /etc/resolv.conf $A/etc/resolv.conf
[ -d $A/src/cmd ] || { mkdir -p $A/src $A/out $A/keys $A/work $A/tmp; cp -a /work/src/. $A/src/; }
export WORK=/work/build KUBEROOT_OUT=$A/out KUBEROOT_KEYS=$A/keys KUBEROOT_DISTRO=router
mkdir -p $WORK
cd $A/src
[ -x $A/out/amd64/kuberoot-router ] && [ -f $A/out/amd64/systemd-boot.efi ] || scripts/build-binaries.sh amd64
chroot $A /usr/bin/env -i PATH=/usr/sbin:/usr/bin:/sbin:/bin TMPDIR=/tmp HOME=/root /bin/sh -c 'cd /src && WORK=/work KUBEROOT_OUT=/out KUBEROOT_DISTRO=router KUBEROOT_VERSION=0.1.1-router.field.20 scripts/build-rootfs.sh amd64'
cd $A/out/amd64
for f in vmlinuz.efi; do curl -fsSL -o $f https://github.com/kuberoot-dev/kuberoot/releases/download/v0.1.0/edge-amd64-$f; done
cd $A/src && scripts/build-initramfs.sh amd64 && scripts/build-bundle.sh amd64
ls -la $A/out/amd64; cat $A/out/amd64/VERSION
echo BUILD-OK
