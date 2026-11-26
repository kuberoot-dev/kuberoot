#!/bin/bash
# Minimal recovery setup: package cache/lists on PVC, no recommended desktop stack.
set -euo pipefail
export TMPDIR=/work/tmp DEBIAN_FRONTEND=noninteractive
mkdir -p /work/tmp /work/apt/cache/archives/partial /work/apt/lists/partial
cat > /etc/apt/apt.conf.d/99-field-pvc <<'EOF'
Dir::Cache "/work/apt/cache";
Dir::State::lists "/work/apt/lists";
APT::Install-Recommends "false";
APT::Install-Suggests "false";
EOF
apt-get update
apt-get install -y --no-install-recommends qemu-system-x86 qemu-utils ovmf asciinema curl ca-certificates python3 python3-pil jq iproute2 iptables fonts-dejavu-core age squashfs-tools cpio zstd genisoimage openssh-client python3-matplotlib procps
apt-get clean
/work/bin/agg --version
/work/bin/kubectl version --client
df -h /work
