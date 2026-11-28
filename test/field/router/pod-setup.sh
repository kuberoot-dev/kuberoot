#!/bin/bash
# Tools of the stand pod: QEMU, disk tools, asciinema and agg for captures,
# kubectl and the Go toolchain. Run once in a fresh pod.
set -euo pipefail
export TMPDIR=/work/tmp DEBIAN_FRONTEND=noninteractive
mkdir -p $TMPDIR /work/bin /work/vms /work/field/{images,raw,casts,img,private} /work/apt-cache/archives/partial /work/toolchain
printf 'Dir::Cache "/work/apt-cache";\n' > /etc/apt/apt.conf.d/99-field-cache
apt-get update -qq
apt-get install -y -qq --no-install-recommends qemu-system-x86 qemu-utils qemu-block-extra ovmf asciinema curl ca-certificates python3 python3-pexpect jq fonts-dejavu-core openssh-client socat git tcpdump iproute2 python3-pil bsdextrautils gdisk dosfstools squashfs-tools e2fsprogs cloud-image-utils cpio xz-utils zstd make gcc libc6-dev > /work/setup-packages.log
curl -fsSL --retry 5 https://api.github.com/repos/asciinema/agg/releases/tags/v1.9.0 > /work/agg-release.json
url=$(jq -r '.assets[] | select(.name == "agg-x86_64-unknown-linux-musl") | .browser_download_url' /work/agg-release.json)
curl -fsL --retry 5 "$url" -o /work/bin/agg; chmod +x /work/bin/agg
curl -fsL --retry 5 https://dl.k8s.io/release/v1.37.2/bin/linux/amd64/kubectl -o /work/bin/kubectl; chmod +x /work/bin/kubectl
ln -sf /work/bin/kubectl /usr/local/bin/kubectl; ln -sf /work/bin/agg /usr/local/bin/agg
[ -x /work/toolchain/go/bin/go ] || curl -fsL --retry 5 https://go.dev/dl/go1.26.5.linux-amd64.tar.gz | tar -xz -C /work/toolchain
which qemu-storage-daemon qemu-system-x86_64 sgdisk mksquashfs agg kubectl; /work/toolchain/go/bin/go version
echo SETUP-OK
