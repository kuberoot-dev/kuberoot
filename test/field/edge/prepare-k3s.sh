#!/bin/bash
# Competitor must run alone; stop the previous DUT before invoking this script.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
require_stand
D=$FIELD_ROOT/k3s
mkdir -p "$D"
KEY=$(cat "$FIELD_ROOT/gateway/key.pub")
cat > "$D/user-data" <<YAML
#cloud-config
hostname: edge-k3s
users:
  - name: observer
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys:
      - $KEY
write_files:
  - path: /etc/rancher/k3s/config.yaml
    content: |
      disable:
        - traefik
        - servicelb
      tls-san:
        - 127.0.0.1
        - 10.78.0.11
runcmd:
  - [bash, -euc, 'curl -fL --retry 3 -o /usr/local/bin/k3s https://github.com/k3s-io/k3s/releases/download/v1.37.2%2Bk3s1/k3s; echo "265b3ee6a3198ef14033c63ef46cbb32370d83795199fb6d1b01f21575eddbda  /usr/local/bin/k3s" | sha256sum -c; chmod +x /usr/local/bin/k3s; curl -fL --retry 3 -o /root/install-k3s.sh https://raw.githubusercontent.com/k3s-io/k3s/v1.37.2%2Bk3s1/install.sh; INSTALL_K3S_SKIP_DOWNLOAD=true sh /root/install-k3s.sh server --disable=traefik --disable=servicelb']
YAML
cat > "$D/network-config" <<'YAML'
version: 2
ethernets:
  lan0:
    match: {macaddress: '52:54:00:78:00:01'}
    set-name: lan0
    dhcp4: true
YAML
printf 'instance-id: field-k3s-v1\nlocal-hostname: edge-k3s\n' > "$D/meta-data"
genisoimage -quiet -output "$D/seed.iso" -volid cidata -joliet -rock "$D/user-data" "$D/meta-data" "$D/network-config"
test ! -e "$D/system.raw"
qemu-img convert -f qcow2 -O raw "$FIELD_ROOT/downloads/ubuntu-24.04-server-cloudimg-amd64.img" "$D/system.raw"
qemu-img resize -f raw "$D/system.raw" 32G
VM_DIR="$D" vm_start 1 "$D/seed.iso"
