#!/bin/sh
# Assembles the read-only root filesystem as a squashfs image: upstream
# Kubernetes, containerd, runc, CNI plugins, kine, the few tools they exec
# (nft, mount, conntrack, mkfs.ext4), and what build-binaries.sh made. No shell,
# no package manager. Runs in the builder's Alpine container.
# Usage: scripts/build-rootfs.sh <arch: amd64|arm64>
set -eu

ARCH=${1:?arch required}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=${WORK:-/work}
OUT=${KUBEROOT_OUT:-$ROOT/out}/$ARCH
CACHE=$WORK/cache/$ARCH
set -a; . "$ROOT/build/components.env"; set +a
VERSION=${KUBEROOT_VERSION:-0.1.0-dev}
DISTRO=${KUBEROOT_DISTRO:-edge}
[ -f "$ROOT/distros/$DISTRO/profile.yaml" ] || { echo "no distribution $DISTRO" >&2; exit 1; }

apk add --no-cache -q curl squashfs-tools tar
mkdir -p "$CACHE"
fetch() { [ -s "$CACHE/$2" ] || curl -fsSL -o "$CACHE/$2" "$1"; }

r=$(mktemp -d)
mkdir -p $r/etc/apk
cp -r /etc/apk/keys $r/etc/apk/
cp /etc/apk/repositories $r/etc/apk/
# The distribution may need more programs than the base (distros/<name>/apk).
extra=""
[ -f "$ROOT/distros/$DISTRO/apk" ] && extra=$(grep -v '^#' "$ROOT/distros/$DISTRO/apk" | tr '\n' ' ')
apk add -q --root $r --initdb --no-scripts --no-cache \
  nftables mount umount conntrack-tools ca-certificates-bundle e2fsprogs $extra
rm -rf $r/etc/apk $r/lib/apk $r/var/cache/apk

for b in kube-apiserver kube-controller-manager kube-scheduler kubelet kube-proxy kubectl; do
  fetch "https://dl.k8s.io/release/$K8S_VERSION/bin/linux/$ARCH/$b" "$b-$K8S_VERSION"
  install -D -m 0755 "$CACHE/$b-$K8S_VERSION" "$r/usr/bin/$b"
done
fetch "https://github.com/containerd/containerd/releases/download/v$CONTAINERD_VERSION/containerd-static-$CONTAINERD_VERSION-linux-$ARCH.tar.gz" "containerd-$CONTAINERD_VERSION.tgz"
tar -xzf "$CACHE/containerd-$CONTAINERD_VERSION.tgz" -C $r/usr bin/containerd bin/containerd-shim-runc-v2 bin/ctr
fetch "https://github.com/opencontainers/runc/releases/download/$RUNC_VERSION/runc.$ARCH" "runc-$RUNC_VERSION"
install -m 0755 "$CACHE/runc-$RUNC_VERSION" $r/usr/bin/runc
fetch "https://github.com/containernetworking/plugins/releases/download/$CNI_VERSION/cni-plugins-linux-$ARCH-$CNI_VERSION.tgz" "cni-$CNI_VERSION.tgz"
mkdir -p $r/usr/libexec/cni
tar -xzf "$CACHE/cni-$CNI_VERSION.tgz" -C $r/usr/libexec/cni ./bridge ./host-local ./loopback ./portmap
fetch "https://github.com/k3s-io/kine/releases/download/$KINE_VERSION/kine-$ARCH" "kine-$KINE_VERSION"
install -m 0755 "$CACHE/kine-$KINE_VERSION" $r/usr/bin/kine
# etcd: the store of a control plane that others join; etcdutl restores its snapshots.
fetch "https://github.com/etcd-io/etcd/releases/download/$ETCD_VERSION/etcd-$ETCD_VERSION-linux-$ARCH.tar.gz" "etcd-$ETCD_VERSION-$ARCH.tgz"
tar -xzf "$CACHE/etcd-$ETCD_VERSION-$ARCH.tgz" -C $r/usr/bin --strip-components=1 \
  "etcd-$ETCD_VERSION-linux-$ARCH/etcd" "etcd-$ETCD_VERSION-linux-$ARCH/etcdutl"

install -m 0755 "$OUT/kinit" $r/usr/sbin/kinit
if [ "${KUBEROOT_STORE_LAB:-}" = 1 ]; then install -m 0755 "$OUT/kuberoot-store" $r/usr/bin/; fi
install -m 0755 "$OUT/kuberoot-node" "$OUT/kuberoot-installer" "$OUT/kuberoot-intents" "$OUT/kuberoot-router" "$OUT/kuberoot-vmctl" "$OUT/kuberoot-aictl" "$OUT/kuberoot-agent" "$OUT/kuberoot-devices" "$OUT/kuberoot-wsctl" "$OUT/kuberoot-fleet" "$OUT/kubepkg" "$OUT/kubepkg-operator" $r/usr/bin/
mkdir -p $r/usr/share/kuberoot/kubepkg/crds
cp "$OUT"/kubepkg-crds/*.yaml $r/usr/share/kuberoot/kubepkg/crds/
cp -r "$OUT/glibc/." $r/
cp -r "$ROOT/rootfs/." $r/
install -m 0644 "$ROOT/distros/$DISTRO/profile.yaml" $r/usr/share/kuberoot/profile.yaml
# The distribution's own add-ons, next to the image's.
if [ -d "$ROOT/distros/$DISTRO/addons" ]; then cp -r "$ROOT/distros/$DISTRO/addons/." $r/usr/share/kuberoot/addons/; fi
install -m 0644 "$OUT/release.pub" $r/usr/share/kuberoot/release.pub
# The distribution may fetch more (distros/<name>/extras.sh), with fetch,
# $CACHE, $ARCH and the root at $r.
if [ -f "$ROOT/distros/$DISTRO/extras.sh" ]; then . "$ROOT/distros/$DISTRO/extras.sh"; fi
# Out-of-tree modules built and signed with this kernel (build-drbd.sh).
[ -d "$OUT/modules" ] && cp -r "$OUT/modules/." $r/lib/modules/
printf 'NAME="kuberoot"\nID=kuberoot\nPRETTY_NAME="kuberoot %s (%s)"\nVERSION_ID=%s\nHOME_URL="https://github.com/kuberoot-dev/kuberoot"\n' "$VERSION" "$DISTRO" "$VERSION" > $r/etc/os-release
# /lib/modules: agents (Cilium) mount it from the host, even with no modules.
mkdir -p $r/dev $r/proc $r/sys $r/run $r/tmp $r/var $r/etc/kubernetes $r/etc/cni/net.d $r/opt/cni/bin $r/lib/modules
rm -f "$OUT/rootfs.squashfs"
mksquashfs $r "$OUT/rootfs.squashfs" -comp zstd -all-root -noappend -quiet
rm -rf $r
echo "$VERSION" > "$OUT/VERSION"
echo "rootfs: $OUT/rootfs.squashfs ($(du -h "$OUT/rootfs.squashfs" | cut -f1)), $DISTRO $VERSION"
