# Sourced by the Linux VMM build and Alpine image assembly. ROOT is the
# Kuberoot checkout. Do not source a manifest supplied with cached binaries.
. "$ROOT/build/cloud-hypervisor/pin.env"

vmm_verify_inputs() {
  printf '%s  %s\n' "$VMM_PATCH_SHA256" "$ROOT/build/cloud-hypervisor/kuberoot.patch" | sha256sum -c - >&2
  VMM_SOURCE_ID=$(printf '%s\n' "$VMM_UPSTREAM_SHA256" "$VMM_PATCH_SHA256" "$VMM_CARGO_LOCK_SHA256" | sha256sum | awk '{print $1}')
  # A changed recipe must not install an artifact produced by an older one.
  VMM_RECIPE_ID=$(sha256sum "$ROOT/build/cloud-hypervisor/pin.env" "$ROOT/scripts/build-vmm.sh" "$ROOT/scripts/vmm-contract.sh" | awk '{print $1}' | sha256sum | awk '{print $1}')
}

vmm_build_contract() {
  printf '%s\n' 'schema_version=1' "source_id=$VMM_SOURCE_ID" "recipe_id=$VMM_RECIPE_ID" \
    'architecture=arm64' "target=$VMM_TARGET" "rust_version=$VMM_RUST_VERSION" \
    'profile=release' 'features=kvm' 'default_features=false' \
    'rustflags=-C target-feature=+crt-static' \
    'release_codegen_units=1' 'release_lto=true' 'release_opt_level=s' \
    'release_strip=true' 'release_debug=0' \
    'release_panic=unwind' 'release_debug_assertions=false' 'release_overflow_checks=false' \
    'extra_version=kuberoot' 'cflags=' 'arflags=' \
    'capability=arm-gicv2-acpi-cpu-hotadd-v1'
}

vmm_verify_artifact() {
  vmm_artifact_dir=$1
  vmm_build_contract | cmp - "$vmm_artifact_dir/build-contract"
  # Only the two named binaries may be read by the checksum checker.
  awk 'NF != 2 || length($1) != 64 || $1 ~ /[^0-9a-f]/ {exit 1}
    $2 == "cloud-hypervisor" {ch++; next}
    $2 == "ch-remote" {remote++; next}
    {exit 1}
    END {if (NR != 2 || ch != 1 || remote != 1) exit 1}' "$vmm_artifact_dir/binaries.sha256"
  (cd "$vmm_artifact_dir" && sha256sum -c binaries.sha256)
  awk 'NF != 2 || length($1) != 64 || $1 ~ /[^0-9a-f]/ {exit 1}
    $2 == "build-contract" {contract++; next}
    $2 == "toolchain.txt" {toolchain++; next}
    $2 == "build-manifest.json" {manifest++; next}
    {exit 1}
    END {if (NR != 3 || contract != 1 || toolchain != 1 || manifest != 1) exit 1}' "$vmm_artifact_dir/metadata.sha256"
  (cd "$vmm_artifact_dir" && sha256sum -c metadata.sha256)
}
