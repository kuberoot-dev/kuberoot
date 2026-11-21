#!/usr/bin/env bash
# Builds the Kubernetes binaries the root filesystem carries from the
# release's source, for an architecture dl.k8s.io publishes none for
# (riscv64). Static, without cgo, stamped with the release's version as the
# upstream build stamps it. Runs in the builder; the source stays in $WORK.
# Named binaries are built alone, next to those already built of the same
# version, for a builder short of disk to empty its Go cache in between;
# VERSION is written once all of them are there.
# Usage: scripts/build-kubernetes.sh <arch> [binary...]
set -euo pipefail

ARCH=${1:?arch required}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=${WORK:-/work}
OUT=${KUBEROOT_OUT:-$ROOT/out}/$ARCH/kubernetes
K8S_VERSION=$(sed -n 's/^K8S_VERSION=//p' "$ROOT/build/components.env")
BINARIES="kube-apiserver kube-controller-manager kube-scheduler kubelet kube-proxy kubectl"
shift
want=${*:-$BINARIES}

if [ "$(cat "$OUT/VERSION" 2>/dev/null)" = "$K8S_VERSION" ]; then
  echo "kubernetes: $K8S_VERSION for $ARCH already built"
  exit 0
fi

# Serialize with the shared-runtime build, which temporarily overlays source.
mkdir -p "$WORK"
exec 9>"$WORK/kubernetes-source.lock"
flock 9

# Keep downloads, compiler temporaries and package caches on the builder PVC.
export GOCACHE=$WORK/kubernetes-cache-$ARCH TMPDIR=$WORK/kubernetes-tmp-$ARCH
mkdir -p "$GOCACHE" "$TMPDIR"
trap 'rm -rf "$GOCACHE" "$TMPDIR"' EXIT

# The release's source archive, checked against the digest published with it.
src=$WORK/src/kubernetes-$K8S_VERSION
if [ ! -d "$src" ]; then
  tmp=$(mktemp -d)
  curl -fsSL -o "$tmp/src.tgz" "https://dl.k8s.io/release/$K8S_VERSION/kubernetes-src.tar.gz"
  echo "$(curl -fsSL "https://dl.k8s.io/release/$K8S_VERSION/kubernetes-src.tar.gz.sha256")  $tmp/src.tgz" | sha256sum -c --quiet
  mkdir -p "$src.new"
  tar -xzf "$tmp/src.tgz" -C "$src.new"
  rm -rf "$tmp"
  mv "$src.new" "$src"
fi

# What hack/lib/version.sh stamps into a release build.
v=${K8S_VERSION#v}
major=${v%%.*}
minor=${v#*.}
minor=${minor%%.*}
date=$(date -u -d "@$(stat -c %Y "$src/go.mod")" +%Y-%m-%dT%H:%M:%SZ)
ldflags="-s -w"
for pkg in k8s.io/client-go/pkg/version k8s.io/component-base/version; do
  ldflags="$ldflags -X $pkg.gitVersion=$K8S_VERSION -X $pkg.gitMajor=$major -X $pkg.gitMinor=$minor"
  ldflags="$ldflags -X $pkg.gitTreeState=archive -X $pkg.buildDate=$date"
done

# Binaries of another version go.
if [ "$(cat "$OUT/SOURCE" 2>/dev/null)" != "$K8S_VERSION" ]; then
  rm -rf "$OUT"
  mkdir -p "$OUT"
  echo "$K8S_VERSION" >"$OUT/SOURCE"
fi
# Keep both compiled packages and compiler temporaries on WORK. Building one
# command at a time and dropping its cache bounds disk use on the 60 GiB PVC.
for b in $want; do
  case " $BINARIES " in *" $b "*) ;; *) echo "unknown Kubernetes binary: $b" >&2; exit 1 ;; esac
  (cd "$src" && GOFLAGS=-buildvcs=false GOOS=linux GOARCH=$ARCH CGO_ENABLED=0 \
    go build -p "${GO_BUILD_PARALLELISM:-4}" -trimpath -ldflags="$ldflags" -o "$OUT/$b" "./cmd/$b")
  rm -rf "$GOCACHE" "$TMPDIR"
  mkdir -p "$GOCACHE" "$TMPDIR"
done
missing=""
for b in $BINARIES; do [ -f "$OUT/$b" ] || missing="$missing $b"; done
[ -z "$missing" ] || { echo "kubernetes: built $want; still to build:$missing"; exit 0; }
echo "$K8S_VERSION" >"$OUT/VERSION"
echo "kubernetes: $K8S_VERSION for $ARCH from source -> $OUT ($(du -sh "$OUT" | cut -f1))"
