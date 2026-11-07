#!/usr/bin/env bash
# Build the reviewed ARM port on a Linux builder; never use a moving checkout.
# Usage: scripts/build-vmm.sh arm64 [--prepare-only]
# CROSS_LINKER can select an isolated GCC 12 wrapper; it accepts no arguments.
set -euo pipefail

ARCH=${1:?architecture required}
[[ "$ARCH" == arm64 ]] || { echo 'the custom VMM build is arm64 only' >&2; exit 1; }
[[ ${2:-} == '' || ${2:-} == --prepare-only ]] && [[ $# -le 2 ]] || { echo 'unexpected build argument' >&2; exit 1; }
[[ $(uname -s) == Linux ]] || { echo 'run the VMM build on the remote Linux builder' >&2; exit 1; }
ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=${WORK:-/work}
OUT=${KUBEROOT_OUT:-$ROOT/out}/$ARCH
export LC_ALL=C
. "$ROOT/scripts/vmm-contract.sh"
for tool in curl sha256sum tar git python3 flock; do command -v "$tool" >/dev/null; done
vmm_verify_inputs
state="$WORK/vmm-build"
mkdir -p "$state/downloads" "$state/sources" "$OUT"
exec 9>"$state/build.lock"
flock 9

archive="$state/downloads/$VMM_UPSTREAM_SHA256.tar.gz"
temporary=''
trap 'if [[ -n "$temporary" ]]; then rm -rf -- "$temporary"; fi' EXIT
if [[ ! -f "$archive" ]]; then
  temporary=$(mktemp "$state/downloads/source.XXXXXX")
  curl --fail --location --silent --show-error "$VMM_UPSTREAM_URL" -o "$temporary"
  printf '%s  %s\n' "$VMM_UPSTREAM_SHA256" "$temporary" | sha256sum -c -
  mv "$temporary" "$archive"
  temporary=''
fi
printf '%s  %s\n' "$VMM_UPSTREAM_SHA256" "$archive" | sha256sum -c -
source="$state/sources/$VMM_SOURCE_ID"
source_manifest="$state/sources/$VMM_SOURCE_ID.sha256"
source_checksums() {
  # The pinned base and reviewed patch contain only directories/regular files.
  # Reject new links and compare the entire file set, not only known entries.
  if [[ -n $(find "$1" ! -type f ! -type d -print -quit) ]]; then
    echo 'unexpected non-regular entry in the prepared VMM source' >&2
    return 1
  fi
  (cd "$1" && find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum)
}
if [[ ! -e "$source" ]]; then
  temporary=$(mktemp -d "$state/sources/preparing.XXXXXX")
  tar -xzf "$archive" --strip-components=1 --no-same-owner -C "$temporary"
  git -C "$temporary" apply --check "$ROOT/build/cloud-hypervisor/kuberoot.patch"
  git -C "$temporary" apply "$ROOT/build/cloud-hypervisor/kuberoot.patch"
  printf '%s  %s\n' "$VMM_CARGO_LOCK_SHA256" "$temporary/Cargo.lock" | sha256sum -c -
  source_checksums "$temporary" > "$source_manifest.new"
  mv "$temporary" "$source"
  temporary=''
  mv "$source_manifest.new" "$source_manifest"
fi
source_checksums "$source" > "$state/source-verification.sha256"
cmp "$source_manifest" "$state/source-verification.sha256"
printf 'verified source %s\n' "$VMM_SOURCE_ID"
if [[ ${2:-} == --prepare-only ]]; then exit 0; fi

linker=$(command -v "${CROSS_LINKER:-aarch64-linux-gnu-gcc}")
# Keep compiler/proxy access, but exclude ambient Cargo/cc-rs configuration.
# Cache directories may be shared; their parent Cargo config is never loaded.
build_env=(env -i "PATH=$PATH" "HOME=$HOME" LC_ALL=C)
for name in RUSTUP_HOME LD_LIBRARY_PATH HTTP_PROXY HTTPS_PROXY ALL_PROXY NO_PROXY http_proxy https_proxy all_proxy no_proxy SSL_CERT_FILE SSL_CERT_DIR; do
  if value=$(printenv "$name"); then build_env+=("$name=$value"); fi
done
compiler() { "${build_env[@]}" "$linker" "$@"; }
gcc_version=$(compiler -dumpversion)
[[ ${gcc_version%%.*} == 12 ]] || { echo 'cross GCC major version 12 is required' >&2; exit 1; }
[[ $(compiler -dumpmachine) == aarch64-linux-gnu ]] || { echo 'wrong cross compiler target' >&2; exit 1; }
rustc +"$VMM_RUST_VERSION" --version | grep -q "^rustc $VMM_RUST_VERSION "
rustup target list --installed --toolchain "$VMM_RUST_VERSION" | grep -qx "$VMM_TARGET"
rustc_binary=$(rustup which --toolchain "$VMM_RUST_VERSION" rustc)
cargo_binary=$(rustup which --toolchain "$VMM_RUST_VERSION" cargo)
archiver=$(command -v "$(compiler -print-prog-name=ar)")
shared_cargo_home=${CARGO_HOME:-$HOME/.cargo}
cargo_home="$state/cargo-home"
mkdir -p "$cargo_home" "$state/tmp"
for name in registry git; do
  if [[ ! -e "$cargo_home/$name" && -d "$shared_cargo_home/$name" ]]; then
    ln -s "$shared_cargo_home/$name" "$cargo_home/$name"
  fi
done
[[ ! -e "$cargo_home/config" && ! -e "$cargo_home/config.toml" ]] || { echo 'private Cargo home contains unexpected config' >&2; exit 1; }
parent="$source"
while :; do
  [[ ! -e "$parent/.cargo/config" && ! -e "$parent/.cargo/config.toml" ]] || { echo "unexpected ancestor Cargo config: $parent" >&2; exit 1; }
  [[ "$parent" != / ]] || break
  parent=$(dirname "$parent")
done
toolchain=$(mktemp "$state/toolchain.XXXXXX")
{
  "$rustc_binary" -Vv
  "$cargo_binary" --version
  compiler --version
  compiler -dumpspecs
  sha256sum "$rustc_binary" "$cargo_binary" "$linker" "$archiver"
  driver=$(compiler -v 2>&1 | sed -n 's/^COLLECT_GCC=//p')
  sha256sum "$(command -v "$driver")"
  for program in cc1 collect2 as ld; do
    path=$(command -v "$(compiler -print-prog-name="$program")")
    sha256sum "$path"
  done
  # Static system libraries affect the result even when GCC's version is unchanged.
  for library in libc.a libm.a libgcc.a libgcc_eh.a crt1.o crti.o crtn.o crtbeginT.o; do
    path=$(compiler -print-file-name="$library")
    [[ -f "$path" ]] || { echo "missing cross compiler input: $library" >&2; exit 1; }
    sha256sum "$path"
  done
  for library in libpthread.a libdl.a librt.a libutil.a Scrt1.o rcrt1.o crtbegin.o crtbeginS.o crtend.o crtendS.o; do
    path=$(compiler -print-file-name="$library")
    if [[ -f "$path" ]]; then sha256sum "$path"; else printf 'absent optional input %s\n' "$library"; fi
  done
} > "$toolchain"
toolchain_sha=$(sha256sum "$toolchain" | awk '{print $1}')
build_id=$(printf '%s\n' "$VMM_SOURCE_ID" "$VMM_RECIPE_ID" "$toolchain_sha" | sha256sum | awk '{print $1}')
target_dir="$state/targets/$build_id"
mkdir -p "$target_dir"
mv "$toolchain" "$target_dir/toolchain.txt"

# cc-rs compiles ring's C/assembly as well as Rust's final link. Select both
# tools explicitly; an inherited CC/CFLAGS/profile must not change semantics.
build_env+=("CARGO_HOME=$cargo_home" "TMPDIR=$state/tmp" "RUSTC=$rustc_binary" "RUSTUP_TOOLCHAIN=$VMM_RUST_VERSION"
  CARGO_INCREMENTAL=0 "CARGO_TARGET_DIR=$target_dir" "CARGO_TARGET_AARCH64_UNKNOWN_LINUX_GNU_LINKER=$linker"
  CARGO_PROFILE_RELEASE_CODEGEN_UNITS=1 CARGO_PROFILE_RELEASE_LTO=true CARGO_PROFILE_RELEASE_OPT_LEVEL=s
  CARGO_PROFILE_RELEASE_STRIP=true CARGO_PROFILE_RELEASE_DEBUG=0 CARGO_PROFILE_RELEASE_PANIC=unwind
  CARGO_PROFILE_RELEASE_DEBUG_ASSERTIONS=false CARGO_PROFILE_RELEASE_OVERFLOW_CHECKS=false
  'RUSTFLAGS=-C target-feature=+crt-static' CH_EXTRA_VERSION=kuberoot
  "CC_aarch64_unknown_linux_gnu=$linker" "AR_aarch64_unknown_linux_gnu=$archiver"
  CFLAGS_aarch64_unknown_linux_gnu= ARFLAGS_aarch64_unknown_linux_gnu=)
(cd "$source" && "${build_env[@]}" "$cargo_binary" build --locked --release --target "$VMM_TARGET" \
  --no-default-features --features kvm --bin cloud-hypervisor --bin ch-remote -j "${JOBS:-1}")

temporary=$(mktemp -d "$OUT/vmm.new.XXXXXX")
for binary in cloud-hypervisor ch-remote; do
  path="$target_dir/$VMM_TARGET/release/$binary"
  [[ -s "$path" ]] || { echo "empty VMM artifact: $binary" >&2; exit 1; }
  readelf -h "$path" | grep -q 'Machine:.*AArch64'
  if readelf -l "$path" | grep -q INTERP || readelf -d "$path" 2>/dev/null | grep -q NEEDED; then
    echo "VMM artifact must be statically linked: $binary" >&2
    exit 1
  fi
  install -m 0755 "$path" "$temporary/$binary"
done
source_checksums "$source" > "$target_dir/source-after-build.sha256"
cmp "$source_manifest" "$target_dir/source-after-build.sha256"
vmm_build_contract > "$temporary/build-contract"
(cd "$temporary" && sha256sum cloud-hypervisor ch-remote > binaries.sha256)
cp "$target_dir/toolchain.txt" "$temporary/toolchain.txt"
python3 - "$temporary" "$VMM_UPSTREAM_COMMIT" "$VMM_SOURCE_ID" "$VMM_RECIPE_ID" "$build_id" "$toolchain_sha" <<'PY'
from pathlib import Path
import hashlib, json, sys
directory = Path(sys.argv[1])
manifest = dict(zip(('base_commit', 'source_id', 'recipe_id', 'build_id', 'toolchain_sha256'), sys.argv[2:]))
manifest['schema_version'] = 1
manifest['contract'] = (directory / 'build-contract').read_text().splitlines()
manifest['binaries_sha256'] = {name: hashlib.sha256((directory / name).read_bytes()).hexdigest() for name in ('cloud-hypervisor', 'ch-remote')}
(directory / 'build-manifest.json').write_text(json.dumps(manifest, sort_keys=True, indent=2) + '\n')
PY
(cd "$temporary" && sha256sum build-contract toolchain.txt build-manifest.json > metadata.sha256)
vmm_verify_artifact "$temporary"
# Keep a previous output for inspection; only this build owns this output name.
if [[ -e "$OUT/vmm" ]]; then
  backup=$(mktemp -d "$OUT/vmm.previous.XXXXXX")
  rmdir "$backup"
  mv "$OUT/vmm" "$backup"
fi
mv "$temporary" "$OUT/vmm"
temporary=''
printf 'VMM release build %s: %s\n' "$build_id" "$OUT/vmm"
