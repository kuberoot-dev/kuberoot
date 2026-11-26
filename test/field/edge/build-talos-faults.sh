#!/bin/bash
# Execute only between measurements: real installer images, no Docker/chroot.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
require_stand
B=$FIELD_ROOT/talos-fault-build
mkdir -p "$B/bin" "$B/assets"
export GOMAXPROCS=2 GOMEMLIMIT=1GiB GOCACHE=$B/gocache CGO_ENABLED=0
"$FIELD_ROOT/build-main/go/bin/go" build -trimpath -ldflags='-s -w' -o "$B/fixture-agent" "$FIELD_SCRIPTS/fixture-agent.go"
python3 "$FIELD_SCRIPTS/oci-image.py" fetch ghcr.io/siderolabs/imager:v1.14.7 "$B/imager-oci" > "$B/imager-pin.json"
python3 "$FIELD_SCRIPTS/oci-image.py" extract-file "$B/imager-oci" usr/bin/installer "$B/bin/imager"
for asset in vmlinuz initramfs.xz systemd-stub.efi systemd-boot.efi; do
 python3 "$FIELD_SCRIPTS/oci-image.py" extract-file "$B/imager-oci" "usr/install/amd64/$asset" "$B/assets/$asset"
done
sha256sum "$B/bin/imager" "$B/assets/"* > "$B/extracted-sha256.txt"
# Pinned vendor blobs are downloadable; the original digest manifest remains.
rm -rf -- "$B/imager-oci/blobs"
python3 "$FIELD_SCRIPTS/oci-image.py" fetch ghcr.io/siderolabs/installer-base:v1.14.7 "$B/base-oci" > "$B/base-pin.json"
# Bound helper parallelism to the pod budget, even when tools detect host CPUs.
cat > "$B/bin/mksquashfs" <<'WRAP'
#!/bin/bash
exec /usr/bin/mksquashfs "$@" -processors 2 -mem 256M
WRAP
cat > "$B/bin/zstd" <<'WRAP'
#!/bin/bash
args=("$@");exec /usr/bin/zstd "${args[@]/-T0/-T2}"
WRAP
chmod +x "$B/bin/"*
export PATH=$B/bin:$PATH
for mode in init-crash pod-network wrong-content incompatible-state; do
 python3 "$FIELD_SCRIPTS/prepare-talos-fault-extension.py" "$B/fixture-agent" "$mode" "$B/$mode"
 cat > "$B/$mode/profile.yaml" <<YAML
arch: amd64
platform: metal
secureboot: false
version: 1.14.7
input:
  kernel: {path: $B/assets/vmlinuz}
  initramfs: {path: $B/assets/initramfs.xz}
  sdStub: {path: $B/assets/systemd-stub.efi}
  sdBoot: {path: $B/assets/systemd-boot.efi}
  baseInstaller: {ociPath: $B/base-oci}
  systemExtensions:
    - ociPath: $B/$mode/oci
output:
  kind: installer
  outFormat: raw
YAML
 "$B/bin/imager" - --output "$B/$mode/out" < "$B/$mode/profile.yaml" > "$B/$mode/build.log" 2>&1
 python3 "$FIELD_SCRIPTS/oci-image.py" import-docker "$B/$mode/out/installer-amd64.tar" "$B/$mode/installer-oci" > "$B/$mode/installer-pin.json"
 rm -- "$B/$mode/out/installer-amd64.tar"
done
rm -rf -- "$B/gocache"
