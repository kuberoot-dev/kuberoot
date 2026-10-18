#!/usr/bin/env bash
# Packs the current build into a signed release bundle that the node API
# Upgrade resource installs. Runs in the builder.
# Usage: scripts/build-bundle.sh <arch: amd64|arm64>
set -euo pipefail

ARCH=${1:?arch required}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=${WORK:-/work}
OUT=${KUBEROOT_OUT:-$ROOT/out}/$ARCH
VERSION=$(cat "$OUT/VERSION")
BUNDLE="$OUT/bundles/kuberoot-$VERSION-$ARCH.tar"

mkdir -p "$OUT/bundles"
files="VERSION vmlinuz.efi initrd.cpio rootfs.squashfs"
# The kernel's own boot arguments, where it cannot carry them (arm64).
[ -f "$OUT/kernel.args" ] && files="$files kernel.args"
tar -C "$OUT" -cf "$BUNDLE" $files
sha256sum "$BUNDLE" | cut -d' ' -f1 > "$BUNDLE.sha256"
# Nodes install only bundles signed with a key their image trusts.
(cd "$ROOT" && go run ./cmd/kuberoot-release sign "$BUNDLE" "${KUBEROOT_RELEASE_KEY:-${KUBEROOT_KEYS:-$WORK/keys}/release.key}" >/dev/null)
echo "bundle: $BUNDLE, signed, sha256 $(cat "$BUNDLE.sha256")"
