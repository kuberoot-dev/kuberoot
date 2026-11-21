#!/usr/bin/env bash
# Build inside the pinned Kubernetes source tree on the remote builder.
# WORK contains the source, all temporary files, and the disposable Go cache.
set -euo pipefail
ARCH=${1:?architecture required}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=${WORK:-/work}
OUT=${KUBEROOT_OUT:-$ROOT/out}/$ARCH
kine=$(sed -n 's/^KINE_VERSION=//p' "$ROOT/build/components.env")
version=$(sed -n 's/^K8S_VERSION=//p' "$ROOT/build/components.env")
v=${version#v}
major=${v%%.*}
minor=${v#*.}; minor=${minor%%.*}
src=$WORK/src/kubernetes-$version
export GOCACHE=$WORK/slim-gocache TMPDIR=$WORK/slim-tmp
mkdir -p "$WORK"
# Both source build scripts hold this lock: the overlay must never become
# visible to another Kubernetes compilation using the same WORK directory.
exec 9>"$WORK/kubernetes-source.lock"
flock 9
mkdir -p "$GOCACHE" "$TMPDIR" "$OUT"
if [ ! -d "$src" ]; then
 mkdir -p "$WORK/src"
 curl -fsSL "https://dl.k8s.io/release/$version/kubernetes-src.tar.gz" -o "$TMPDIR/src.tgz"
 echo "$(curl -fsSL "https://dl.k8s.io/release/$version/kubernetes-src.tar.gz.sha256")  $TMPDIR/src.tgz" | sha256sum -c
 rm -rf "$src.new"
 mkdir -p "$src.new"
 tar -xzf "$TMPDIR/src.tgz" -C "$src.new"
 mv "$src.new" "$src"
 rm "$TMPDIR/src.tgz"
fi
signal=$src/staging/src/k8s.io/apiserver/pkg/server/signal.go
cp "$signal" "$TMPDIR/signal.go"
cp "$src/go.mod" "$TMPDIR/go.mod"
cp "$src/go.sum" "$TMPDIR/go.sum"
cleanup() { cp "$TMPDIR/signal.go" "$signal"; cp "$TMPDIR/go.mod" "$src/go.mod"; cp "$TMPDIR/go.sum" "$src/go.sum"; rm -rf "$src/cmd/kuberoot-control" "$GOCACHE" "$TMPDIR"; }
trap cleanup EXIT
python3 "$ROOT/build/kubernetes-slim/overlay.py" "$src"
mkdir -p "$src/cmd/kuberoot-control"
cp "$ROOT/build/kubernetes-slim/main.go.in" "$src/cmd/kuberoot-control/main.go"
# SQLite uses the same C implementation as the standalone kine release.
# netgo/osusergo avoid libc DNS/user lookups in the static binary.
# DWARF is stripped from the result anyway; not generating it keeps the
# package cache and temporary objects within the builder PVC budget.
case "$ARCH" in
  amd64) karch=x86_64 ;;
  arm64) karch=arm64 ;;
  riscv64) karch=riscv ;;
  *) echo "unknown architecture: $ARCH" >&2; exit 1 ;;
esac
. "$ROOT/scripts/kernel-flavour.sh"
kernel_cross "$karch"
export CC=${CC:-${CROSS_COMPILE:-}gcc}
# Bookworm's GNU linker and lld 14 cannot link the newer riscv64 libc
# relocations. Select a current lld explicitly, including for GCC's search.
linker=""
for tool in ld.lld-21 ld.lld-20 ld.lld-19 ld.lld-18 ld.lld; do
  candidate=$(command -v "$tool" || true)
  [ -n "$candidate" ] || continue
  level=$("$candidate" --version | awk '{for (i=1;i<NF;i++) if ($i=="LLD") {split($(i+1),v,"."); print v[1]; exit}}')
  [ "$level" -ge 18 ] || continue
  linker=$candidate; break
done
[ -n "$linker" ] || { echo "build-slim: install LLVM lld >=18 in the builder" >&2; exit 1; }
mkdir -p "$TMPDIR/linker"
ln -s "$linker" "$TMPDIR/linker/ld.lld"
export GOWORK=off
(cd "$src" && go mod edit -require="github.com/k3s-io/kine@$kine")
stamp=$(date -u +%Y-%m-%dT%H:%M:%SZ)
ldflags="-s -w -linkmode external -extldflags '-static -B$TMPDIR/linker/ -fuse-ld=lld'"
for pkg in k8s.io/component-base/version k8s.io/client-go/pkg/version; do
  ldflags="$ldflags -X $pkg.gitVersion=$version -X $pkg.gitMajor=$major -X $pkg.gitMinor=$minor"
  ldflags="$ldflags -X $pkg.gitTreeState=archive -X $pkg.buildDate=$stamp"
done
(cd "$src" && GOFLAGS='-buildvcs=false -mod=mod' GOOS=linux GOARCH=$ARCH CGO_ENABLED=1 \
  go build -tags=netgo,osusergo,sqlite_omit_load_extension -p 4 -gcflags=all=-dwarf=false \
    -trimpath -ldflags="$ldflags" -o "$OUT/kuberoot-control" ./cmd/kuberoot-control)
echo "slim: Kubernetes $version + kine $kine -> $OUT/kuberoot-control"
