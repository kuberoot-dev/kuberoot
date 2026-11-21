#!/bin/sh
# Packs the pause program into an OCI image archive, pause.tar, with its
# reference in pause.ref, for kinit to import into containerd (images.go).
# One layer holding /pause, run as nobody, as the upstream image runs it.
# The archive's content depends only on the program, so a rebuild of the same
# program is the same image.
# Usage: scripts/pause-image.sh <pause binary> <arch> <dest dir>
set -eu

bin=${1:?pause binary required}
arch=${2:?arch required}
dest=${3:?destination required}
# Changes when the program does; kinit takes whatever pause.ref says.
REF=localhost/kuberoot/pause:1

d=$(mktemp -d)
mkdir -p "$d/root" "$d/oci/blobs/sha256"
install -m 0755 "$bin" "$d/root/pause"
tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner -C "$d/root" -cf "$d/layer.tar" pause

# blob <file>: moves the file into the layout under its digest, prints "<digest> <size>".
blob() {
  sum=$(sha256sum "$1" | cut -d' ' -f1)
  size=$(wc -c <"$1" | tr -d ' ')
  mv "$1" "$d/oci/blobs/sha256/$sum"
  echo "$sum $size"
}
set -- $(blob "$d/layer.tar")
layer=$1 layer_size=$2
printf '{"architecture":"%s","os":"linux","config":{"User":"65535:65535","Entrypoint":["/pause"]},"rootfs":{"type":"layers","diff_ids":["sha256:%s"]}}' \
  "$arch" "$layer" >"$d/config.json"
set -- $(blob "$d/config.json")
config=$1 config_size=$2
printf '{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:%s","size":%s},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar","digest":"sha256:%s","size":%s}]}' \
  "$config" "$config_size" "$layer" "$layer_size" >"$d/manifest.json"
set -- $(blob "$d/manifest.json")
manifest=$1 manifest_size=$2
printf '{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:%s","size":%s,"platform":{"architecture":"%s","os":"linux"},"annotations":{"io.containerd.image.name":"%s"}}]}' \
  "$manifest" "$manifest_size" "$arch" "$REF" >"$d/oci/index.json"
printf '{"imageLayoutVersion":"1.0.0"}' >"$d/oci/oci-layout"

mkdir -p "$dest"
tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner -C "$d/oci" -cf "$dest/pause.tar" oci-layout index.json blobs
echo "$REF" >"$dest/pause.ref"
rm -rf "$d"
echo "pause image: $REF ($arch, sha256:$manifest)"
