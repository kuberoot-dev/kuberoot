# Sourced by build-rootfs.sh: cloud-hypervisor, the virtual machine monitor,
# and its UEFI firmware.
r=${r:?rootfs directory is required}
case $ARCH in
  amd64)
    fetch "https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/$CLOUD_HYPERVISOR_VERSION/cloud-hypervisor-static" "cloud-hypervisor-$CLOUD_HYPERVISOR_VERSION"
    install -m 0755 "$CACHE/cloud-hypervisor-$CLOUD_HYPERVISOR_VERSION" "$r/usr/bin/cloud-hypervisor"
    fetch "https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/$CLOUD_HYPERVISOR_VERSION/ch-remote-static" "ch-remote-$CLOUD_HYPERVISOR_VERSION"
    install -m 0755 "$CACHE/ch-remote-$CLOUD_HYPERVISOR_VERSION" "$r/usr/bin/ch-remote"
    ;;
  arm64)
    # The upstream ARM binary cannot implement the reviewed GICv2/CPU path.
    # Missing, stale or corrupt custom artifacts must fail the image build.
    . "$ROOT/scripts/vmm-contract.sh"
    vmm_verify_inputs
    vmm_verify_artifact "$OUT/vmm"
    install -m 0755 "$OUT/vmm/cloud-hypervisor" "$r/usr/bin/cloud-hypervisor"
    install -m 0755 "$OUT/vmm/ch-remote" "$r/usr/bin/ch-remote"
    install -D -m 0644 "$OUT/vmm/build-manifest.json" "$r/usr/share/kuberoot/cloud-hypervisor-build.json"
    ;;
  *) echo "unsupported VMM architecture: $ARCH" >&2; exit 1 ;;
esac
# The firmware is CLOUDHV.fd on x86_64 and CLOUDHV_EFI.fd on aarch64.
case $ARCH in amd64) fw=CLOUDHV.fd;; arm64) fw=CLOUDHV_EFI.fd;; esac
fetch "https://github.com/cloud-hypervisor/edk2/releases/download/$CLOUDHV_FIRMWARE_VERSION/$fw" "$fw-$CLOUDHV_FIRMWARE_VERSION"
install -D -m 0644 "$CACHE/$fw-$CLOUDHV_FIRMWARE_VERSION" "$r/usr/share/kuberoot/CLOUDHV.fd"
