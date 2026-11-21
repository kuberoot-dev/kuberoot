#!/usr/bin/env bash
# Turns builds of the edge distribution into the assets of a GitHub release:
# what a node's boot media is written from, for each architecture built,
# mkimage for the systems that write the media, SHA256SUMS of all of them
# and kuberoot-edge.json, which describes them. The media itself is not an
# asset: whoever writes it (the DevOps trainer's lab, for one) makes it with
# a trust directory of its own, so that only their credentials reach the node.
#
# GitHub release assets have no directories, so a file's asset name carries
# its architecture (edge-amd64-vmlinuz.efi); the manifest gives each one the
# path it has in a build's output (amd64/vmlinuz.efi), which is the layout
# mkimage -artifacts reads.
#
# Usage: hack/release/lab-assets.sh <version> <out> <dest>
#   <out>   a build's output with <arch>/ directories, KUBEROOT_OUT of the
#           builder or of hack/e2e/build-image.sh
#   <dest>  where the assets are written; made if missing
set -euo pipefail

version=${1:?version required}
out=${2:?build output required}
dest=${3:?destination required}
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
k8s=$(sed -n 's/^K8S_VERSION=//p' "$ROOT/build/components.env")
mkdir -p "$dest"

entries=()
add() { # <asset> <path> <arch> [os]
  local f=$dest/$1
  entries+=("$(printf '{"name": "%s", "path": "%s", "arch": "%s"%s, "size": %d, "sha256": "%s"}' \
    "$1" "$2" "$3" "${4:+, \"os\": \"$4\"}" "$(wc -c <"$f" | tr -d ' ')" "$(sha256sum "$f" | cut -d' ' -f1)")")
}

arches=()
for arch in amd64 arm64 riscv64; do
  [ -d "$out/$arch" ] || continue
  built=$(cat "$out/$arch/VERSION")
  [ "$built" = "$version" ] || { echo "$out/$arch was built as $built, not $version" >&2; exit 1; }
  for f in vmlinuz.efi initrd.cpio rootfs.squashfs systemd-boot.efi VERSION kernel.args; do
    if [ ! -f "$out/$arch/$f" ]; then
      [ "$f" = kernel.args ] && continue # only kernels with arguments of their own have it
      echo "$out/$arch/$f is missing" >&2
      exit 1
    fi
    cp "$out/$arch/$f" "$dest/edge-$arch-$f"
    add "edge-$arch-$f" "$arch/$f" "$arch"
  done
  arches+=("$arch")
done
[ ${#arches[@]} -gt 0 ] || { echo "no <arch>/ directory in $out" >&2; exit 1; }

for os in linux darwin windows; do
  for arch in amd64 arm64; do
    name=mkimage-$os-$arch
    [ "$os" = windows ] && name=$name.exe
    (cd "$ROOT" && CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags=-s -o "$dest/$name" ./cmd/mkimage)
    add "$name" "bin/$name" "$arch" "$os"
  done
done

{
  printf '{\n  "distro": "edge",\n  "version": "%s",\n  "kubernetes": "%s",\n  "arches": [%s],\n  "files": [\n' \
    "$version" "$k8s" "$(printf '"%s", ' "${arches[@]}" | sed 's/, $//')"
  for i in "${!entries[@]}"; do
    printf '    %s%s\n' "${entries[$i]}" "$([ "$i" -lt $((${#entries[@]} - 1)) ] && echo ,)"
  done
  printf '  ]\n}\n'
} >"$dest/kuberoot-edge.json"
(cd "$dest" && sha256sum edge-* mkimage-* kuberoot-edge.json >SHA256SUMS)
echo "release assets of $version (${arches[*]}): $dest"
