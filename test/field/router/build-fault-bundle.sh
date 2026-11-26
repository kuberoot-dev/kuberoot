#!/bin/bash
# Run only inside the pod. Fault instrumentation is explicit in the version.
set -euo pipefail
export PATH=/work/toolchain/go/bin:$PATH
export GOCACHE=/work/build-main/gocache GOPATH=/work/build-main/gopath GOMAXPROCS=2 GOFLAGS="-p=2 -buildvcs=false"
mode=${1:?crash, noforward or console}; base_root=${2:?rootfs directory}; version=${3:?unique version}
case $mode in crash|noforward|console);; *) exit 2;; esac
work=/work/field-faults/$version
[[ ! -e $work ]] || { echo 'existing experiment preserved' >&2; exit 1; }
mkdir -p "$work/out/amd64" "$work/tmp"
cp -al "$base_root" "$work/root"
root=$work/root; out=$work/out/amd64
mv "$root/usr/bin/kuberoot-router" "$root/usr/bin/kuberoot-router-real"
if [[ $mode == crash ]]; then
 cat > "$root/usr/bin/kuberoot-router" <<'WRAPPER'
#!/bin/sh
echo 'FIELD INSTRUMENTATION: intentional router crash' >&2
exit 1
WRAPPER
elif [[ $mode == noforward ]]; then
 cat > "$root/usr/bin/kuberoot-router" <<'WRAPPER'
#!/bin/sh
(while :; do echo 0 > /proc/sys/net/ipv4/ip_forward; sleep 0.05; done) &
echo 'FIELD INSTRUMENTATION: service runs, IPv4 forwarding forced off' >&2
exec /usr/bin/kuberoot-router-real "$@"
WRAPPER
else
 mv "$root/usr/bin/kuberoot-router-real" "$root/usr/bin/kuberoot-router"
 cp "$root/usr/share/kuberoot/profile.yaml" "$work/profile.yaml"
 cat >> "$work/profile.yaml" <<'PROFILE'
      - name: field-console
        args:
        - /bin/sh
        - -c
        - exec /bin/sh -i </dev/ttyS0 >/dev/ttyS0 2>&1
PROFILE
 cp --remove-destination "$work/profile.yaml" "$root/usr/share/kuberoot/profile.yaml"
fi
chmod 755 "$root/usr/bin/kuberoot-router"
rm "$root/etc/os-release"
printf 'NAME="kuberoot"\nID=kuberoot\nVERSION_ID=%s\n' "$version" > "$root/etc/os-release"
printf '%s\n' "$version" > "$out/VERSION"
cp "$(dirname "$base_root")/out/amd64/"{vmlinuz.efi,initrd.cpio} "$out/"
mksquashfs "$root" "$out/rootfs.squashfs" -noappend -comp zstd -processors 2
export KUBEROOT_OUT="$work/out" KUBEROOT_KEYS=/work/build-main/keys TMPDIR="$work/tmp"
cd /work/field-fixes
scripts/build-bundle.sh amd64
cp "$out/bundles/"* /work/www/
sha256sum "$out/bundles/"*.tar > "$work/bundle.sha256"
cat "$work/bundle.sha256"
