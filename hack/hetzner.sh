#!/usr/bin/env bash
# Drives the builder pod in the workshop cluster (hack/hetzner/builder.yaml):
# ships the sources and runs builds there, so nothing heavy runs locally.
#
#   hack/hetzner.sh sync                 copy the working tree to the builder
#   hack/hetzner.sh build <arch> [...]   sync, then kernel (if missing),
#                                        binaries, rootfs, initramfs
#   hack/hetzner.sh kernel <arch>        rebuild the kernel
#   hack/hetzner.sh media <arch> [args]  boot media from the last build, with
#                                        kernel arguments; served as <arch>/media.img
#   hack/hetzner.sh repo <recipe-dir>... build kubepkg recipes into the builder's
#                                        registry and a signed test index at repo/index.yaml
#   hack/hetzner.sh builder              the builder pod itself (hetzner/builder.yaml)
#   hack/hetzner.sh node <name> <arch>   a VM booting the current media (hetzner/node.yaml)
#   hack/hetzner.sh console <name>       serial console of a node
#   hack/hetzner.sh kp <cluster> args    kubepkg CLI in the builder against a cluster whose
#                                        kubeconfig is /work/kube/<cluster>.conf
#   hack/hetzner.sh kc <cluster> args    kubectl in the builder against /work/kube/<cluster>.conf
#   hack/hetzner.sh test                 sync, then go test ./... in the builder
#   hack/hetzner.sh sh [cmd...]          a shell (or a command) in the builder
#
# KUBEROOT_NS names the tenant everything lives in (default tenant-kuberoot),
# so a second stand can run beside the first, or beside one left in a bad state.

set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
export KUBECONFIG=${KUBECONFIG_WORKSHOP:-$HOME/workshop.txt}
CTX=${KUBE_CONTEXT:-admin@workshop}
# The tenant the builder and the stand nodes live in. Overridable so a second
# stand can run beside the first, or beside a tenant left in a bad state.
NS=${KUBEROOT_NS:-tenant-kuberoot}
k() { kubectl --context "$CTX" -n "$NS" "$@"; }
tools() { k exec -i builder -c tools -- "$@"; }
alpine() { k exec -i builder -c alpine -- "$@"; }

prepare() {
  tools sh -c 'command -v flex >/dev/null || { apt-get -qq update && apt-get -qq install -y flex bison bc libelf-dev libssl-dev cpio xz-utils zstd >/dev/null; }'
}

# The VMM is Rust cross-compiled for the boards, which the kernel and the
# Go binaries do not need; it is installed only when something asks for it.
# Debian 12 ships the GCC 12 the build contract requires, and the Rust
# version comes from the pin, not from whatever rustup calls stable.
prepare_vmm() {
  local rust target
  rust=$(. "$ROOT/build/cloud-hypervisor/pin.env" && echo "$VMM_RUST_VERSION")
  target=$(. "$ROOT/build/cloud-hypervisor/pin.env" && echo "$VMM_TARGET")
  tools sh -c 'command -v aarch64-linux-gnu-gcc >/dev/null ||
    { apt-get -qq update && apt-get -qq install -y gcc-aarch64-linux-gnu >/dev/null; }'
  # RUSTUP_HOME, CARGO_HOME and the PATH that reaches them come from the
  # builder manifest, which keeps the toolchain on the volume: the container
  # disk is far too small for it, and filling it evicts the whole pod.
  tools sh -c 'command -v rustup >/dev/null ||
    curl -sSf https://sh.rustup.rs |
      sh -s -- -y --no-modify-path --profile minimal --default-toolchain none >/dev/null'
  tools sh -c "rustup toolchain install $rust --profile minimal --target $target >/dev/null"
}

SRC=/work/src/kuberoot

# sync ships the sources: git-tracked and new files, no build output.
#
# The stream crosses the API server, and a tunnel that stalls mid-transfer
# leaves the builder with the tree it already had: the unpack and the swap
# are chained, so a torn transfer changes nothing on the far side. That is
# the safe outcome and also the treacherous one, because whatever builds
# next compiles the previous sources and looks like it worked. So: retry,
# then count what landed, and say so when the counts disagree.
sync() {
  local want got attempt
  want=$(cd "$ROOT" && git ls-files -co --exclude-standard | grep -cv '^SESSION_LOG.md$')
  for attempt in 1 2 3; do
    if (cd "$ROOT" && git ls-files -co --exclude-standard -z | grep -zv '^SESSION_LOG.md$' |
      COPYFILE_DISABLE=1 tar --null -T - -czf -) |
      tools sh -c "rm -rf $SRC.new && mkdir -p $SRC.new && tar -xzf - -C $SRC.new && rm -rf $SRC && mv $SRC.new $SRC"
    then
      got=$(tools sh -c "find $SRC -type f | wc -l" | tr -cd '0-9')
      [ "$got" = "$want" ] && return 0
      echo "hetzner.sh: sync put $got of $want files on the builder (attempt $attempt)" >&2
    else
      echo "hetzner.sh: sync could not reach the builder (attempt $attempt)" >&2
    fi
    sleep 3
  done
  echo "hetzner.sh: the builder does not have the current sources; nothing was built" >&2
  return 1
}
# nproc sees the host's CPUs, while the builder has a much smaller quota.
# Bound compilation by default and forward an explicit override remotely.
VM_API_TRANSITION=${KUBEROOT_VM_API_TRANSITION:-0}
case "$VM_API_TRANSITION" in 0|1) ;; *) echo "KUBEROOT_VM_API_TRANSITION must be 0 or 1" >&2; exit 1 ;; esac
ENV="KUBEROOT_OUT=/work/out WORK=/work JOBS=${JOBS:-4} GOMAXPROCS=${GOMAXPROCS:-4} KUBEROOT_VM_API_TRANSITION=$VM_API_TRANSITION"
case "${1:-}" in
  sync) sync ;;
  kernel)
    prepare; sync
    tools sh -c "$ENV KUBEROOT_DISTRO=${KUBEROOT_DISTRO:-edge} $SRC/scripts/build-kernel.sh $2 && $ENV KUBEROOT_DISTRO=${KUBEROOT_DISTRO:-edge} $SRC/scripts/build-drbd.sh $2" ;;
  build)
    arch=${2:?arch required}
    prepare; sync
    # The kernel is built again when the last one was another distribution's.
    distro=${KUBEROOT_DISTRO:-edge}
    flavour=base; [ -f "$ROOT/distros/$distro/kernel.config" ] && flavour=$distro
    tools sh -c "[ -f /work/out/$arch/vmlinuz.efi ] && [ \"\$(cat /work/out/$arch/kernel.flavour 2>/dev/null || echo base)\" = $flavour ] || { $ENV KUBEROOT_DISTRO=$distro $SRC/scripts/build-kernel.sh $arch && $ENV KUBEROOT_DISTRO=$distro $SRC/scripts/build-drbd.sh $arch; }"
    tools sh -c "cd $SRC && $ENV KUBEROOT_STORE_LAB=${KUBEROOT_STORE_LAB:-} scripts/build-binaries.sh $arch"
    case "$distro" in
      cloud|hypervisor|workstation)
        if [ "$arch" = arm64 ]; then
          prepare_vmm
          tools sh -c "$ENV $SRC/scripts/build-vmm.sh arm64"
        fi ;;
      ai) tools sh -c "$ENV $SRC/scripts/build-llama.sh $arch && $ENV $SRC/scripts/build-nvidia.sh $arch && $ENV $SRC/scripts/build-llama-cuda.sh $arch" ;;
      rt) tools sh -c "$ENV $SRC/scripts/build-rt-tests.sh $arch" ;;
    esac
    alpine sh -c "$ENV KUBEROOT_VERSION=${KUBEROOT_VERSION:-} KUBEROOT_DISTRO=${KUBEROOT_DISTRO:-edge} KUBEROOT_STORE_LAB=${KUBEROOT_STORE_LAB:-} sh $SRC/scripts/build-rootfs.sh $arch"
    tools sh -c "$ENV $SRC/scripts/build-initramfs.sh $arch" ;;
  bundle)
    tools sh -c "cd $SRC && $ENV scripts/build-bundle.sh ${2:?arch required}" ;;
  media)
    arch=${2:?arch required}; shift 2
    sync
    # arm64 media carry the Pi 4 firmware, which is fetched rather than built.
    # Idempotent: it keeps what it already has, so this costs nothing on a
    # builder that has run before.
    if [ "$arch" = arm64 ]; then
      tools sh -c "cd $SRC && KUBEROOT_OUT=/work/out scripts/fetch-rpi-firmware.sh arm64"
    fi
    tools sh -c "cd $SRC && go run ./cmd/mkimage -arch $arch -artifacts /work/out/$arch -trust /work/trust -out /work/out/$arch/media.img.new -args '$*' && mv /work/out/$arch/media.img.new /work/out/$arch/media.img && ls -lh /work/out/$arch/media.img" ;;
  repo)
    shift
    # The recipes travel like the sources; the registry and index are reached
    # by the builder Service's address, which nodes can use without DNS.
    ip=$(k get svc builder -o jsonpath='{.spec.clusterIP}')
    tools sh -c 'rm -rf /work/recipes && mkdir -p /work/recipes'
    for d in "$@"; do
      tar -C "$(dirname "$d")" -czf - "$(basename "$d")" | tools tar -xzf - -C /work/recipes
    done
    tools sh -c "set -e
      [ -x /work/bin/kubepkg ] || GOBIN=/work/bin go install github.com/kuberoot-dev/kubepkg/cmd/kubepkg@v1.3.0
      [ -f /work/keys/repo.key ] || /work/bin/kubepkg repo keygen /work/keys/repo >/dev/null
      rm -rf /work/dist && mkdir -p /work/dist /work/out/repo
      for r in /work/recipes/*; do /work/bin/kubepkg build \$r --registry oci://$ip:5000/kubepkg-packages -o /work/dist --plain-http | tail -1; done
      KUBEPKG_SIGNING_KEY=\$(cat /work/keys/repo.key) /work/bin/kubepkg repo index /work/dist --sign-key-env KUBEPKG_SIGNING_KEY -o /work/out/repo/index.yaml --plain-http | tail -1
      cp /work/keys/repo.pub /work/out/repo/index.pub
      echo index: http://$ip:8080/repo/index.yaml" ;;
  builder)
    sed "s/tenant-kuberoot/$NS/g" "$ROOT/hack/hetzner/builder.yaml" | kubectl --context "$CTX" apply -f - ;;
  node)
    # The templates name the default tenant in their own metadata and in the
    # builder's Service address, so the namespace is substituted here too --
    # otherwise KUBEROOT_NS moves the kubectl calls and leaves the objects
    # pointing at a tenant that may not exist.
    sed "s/NODE/${2:?name required}/g; s/ARCH/${3:?arch required}/g; s/tenant-kuberoot/$NS/g" "$ROOT/hack/hetzner/node.yaml" | kubectl --context "$CTX" apply -f - ;;
  console)
    virtctl --context "$CTX" -n "$NS" console "vm-instance-${2:?name required}" ;;
  kp)
    c=${2:?cluster required}; shift 2
    tools sh -c "KUBECONFIG=/work/kube/$c.conf /work/bin/kubepkg $*" ;;
  kc)
    c=${2:?cluster required}; shift 2
    tools sh -c "KUBECONFIG=/work/kube/$c.conf /work/bin/kubectl $*" ;;
  test)
    sync
    # A filtering pipeline in /bin/sh returned grep's status and hid failing
    # tests. Return the Go command's status directly to the caller.
    tools sh -c "cd $SRC && GOMAXPROCS=${GOMAXPROCS:-4} go test -p ${JOBS:-4} ./..." ;;
  sh)
    shift
    if [ $# -eq 0 ]; then k exec -it builder -c tools -- bash; else tools sh -c "$*"; fi ;;
  # Print the header block, whatever length it has grown to, rather than a
  # line range that silently stops listing commands added after it.
  *) awk 'NR>1 && /^#/ {print; next} NR>1 {exit}' "$0"; exit 1 ;;
esac
