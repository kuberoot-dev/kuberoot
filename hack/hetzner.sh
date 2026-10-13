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
#   hack/hetzner.sh node <name> <arch>   a VM booting the current media (hetzner/node.yaml)
#   hack/hetzner.sh console <name>       serial console of a node
#   hack/hetzner.sh kp <cluster> args    kubepkg CLI in the builder against a cluster whose
#                                        kubeconfig is /work/kube/<cluster>.conf
#   hack/hetzner.sh kc <cluster> args    kubectl in the builder against /work/kube/<cluster>.conf
#   hack/hetzner.sh test                 sync, then go test ./... in the builder
#   hack/hetzner.sh sh [cmd...]          a shell (or a command) in the builder
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
export KUBECONFIG=${KUBECONFIG_WORKSHOP:-$HOME/workshop.txt}
CTX=${KUBE_CONTEXT:-admin@workshop}
NS=tenant-kuberoot
k() { kubectl --context "$CTX" -n "$NS" "$@"; }
tools() { k exec -i builder -c tools -- "$@"; }
alpine() { k exec -i builder -c alpine -- "$@"; }

prepare() {
  tools sh -c 'command -v flex >/dev/null || { apt-get -qq update && apt-get -qq install -y flex bison bc libelf-dev libssl-dev cpio xz-utils zstd >/dev/null; }'
}

sync() {
  # Sources only: git-tracked and new files, no build output.
  (cd "$ROOT" && git ls-files -co --exclude-standard -z | grep -zv '^SESSION_LOG.md$' |
    COPYFILE_DISABLE=1 tar --null -T - -czf -) |
    tools sh -c 'rm -rf /work/src/kuberoot.new && mkdir -p /work/src/kuberoot.new && tar -xzf - -C /work/src/kuberoot.new && rm -rf /work/src/kuberoot && mv /work/src/kuberoot.new /work/src/kuberoot'
}

SRC=/work/src/kuberoot
ENV="KUBEROOT_OUT=/work/out WORK=/work"
case "${1:-}" in
  sync) sync ;;
  kernel)
    prepare; sync
    tools sh -c "$ENV $SRC/scripts/build-kernel.sh $2 && $ENV $SRC/scripts/build-drbd.sh $2" ;;
  build)
    arch=${2:?arch required}
    prepare; sync
    # The kernel is built again when the last one was another distribution's.
    distro=${KUBEROOT_DISTRO:-edge}
    flavour=base; [ -f "$ROOT/distros/$distro/kernel.config" ] && flavour=$distro
    tools sh -c "[ -f /work/out/$arch/vmlinuz.efi ] && [ \"\$(cat /work/out/$arch/kernel.flavour 2>/dev/null || echo base)\" = $flavour ] || { $ENV KUBEROOT_DISTRO=$distro $SRC/scripts/build-kernel.sh $arch && $ENV KUBEROOT_DISTRO=$distro $SRC/scripts/build-drbd.sh $arch; }"
    tools sh -c "cd $SRC && $ENV KUBEROOT_STORE_LAB=${KUBEROOT_STORE_LAB:-} scripts/build-binaries.sh $arch"
    case "$distro" in
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
  node)
    sed "s/NODE/${2:?name required}/g; s/ARCH/${3:?arch required}/g" "$ROOT/hack/hetzner/node.yaml" | kubectl --context "$CTX" apply -f - ;;
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
    tools sh -c "cd $SRC && go test ./... 2>&1 | grep -v 'no test files'" ;;
  sh)
    shift
    if [ $# -eq 0 ]; then k exec -it builder -c tools -- bash; else tools sh -c "$*"; fi ;;
  *) sed -n '2,12p' "$0"; exit 1 ;;
esac
