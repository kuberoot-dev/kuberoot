#!/bin/bash
# Run only inside kr-edge-stand. Root privileges are used inside the gateway VM.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
require_stand
D=$FIELD_ROOT/gateway
mkdir -p "$D"
test -f "$D/key" || ssh-keygen -q -t ed25519 -N '' -f "$D/key"
KEY=$(cat "$D/key.pub")
cat > "$D/user-data" <<YAML
#cloud-config
hostname: edge-gateway
users:
  - name: observer
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys:
      - $KEY
package_update: true
packages: [dnsmasq, nftables, iproute2, curl, python3, iperf3]
write_files:
  - path: /etc/sysctl.d/90-forward.conf
    content: |
      net.ipv4.ip_forward=1
  - path: /etc/dnsmasq.d/field.conf
    content: |
      interface=lan0
      bind-interfaces
      dhcp-range=10.78.0.10,10.78.0.50,255.255.255.0,12h
      dhcp-host=52:54:00:78:00:01,10.78.0.11
      dhcp-option=3,10.78.0.1
      dhcp-option=6,10.78.0.1
      server=10.0.2.3
  - path: /etc/nftables.conf
    content: |
      flush ruleset
      table ip nat {
        chain postrouting { type nat hook postrouting priority srcnat; oifname "wan0" masquerade; }
      }
      table inet impairment {
        chain forward { type filter hook forward priority filter; policy accept; }
      }
runcmd:
  - [sysctl, --system]
  - [systemctl, enable, --now, nftables]
  - [systemctl, restart, dnsmasq]
YAML
cat > "$D/network-config" <<'YAML'
version: 2
ethernets:
  wan0:
    match: {macaddress: '52:54:00:78:ff:01'}
    set-name: wan0
    dhcp4: true
  lan0:
    match: {macaddress: '52:54:00:78:ff:02'}
    set-name: lan0
    addresses: [10.78.0.1/24]
YAML
printf 'instance-id: edge-field-gateway-v1\nlocal-hostname: edge-gateway\n' > "$D/meta-data"
genisoimage -quiet -output "$D/seed.iso" -volid cidata -joliet -rock "$D/user-data" "$D/meta-data" "$D/network-config"
test -f "$D/system.qcow2" || qemu-img create -f qcow2 -F qcow2 -b "$FIELD_ROOT/downloads/debian-12-genericcloud-amd64.qcow2" "$D/system.qcow2" 4G
qemu-system-x86_64 -name edge-field-gateway -enable-kvm -cpu host -smp 1 -m 512 -machine q35 \
 -drive "file=$D/system.qcow2,format=qcow2,if=virtio" -drive "file=$D/seed.iso,format=raw,if=virtio,readonly=on" \
 -netdev user,id=wan,hostfwd=tcp:127.0.0.1:2222-:22 \
 -device virtio-net-pci,netdev=wan,mac=52:54:00:78:ff:01 \
 -netdev socket,id=lan,mcast=230.78.0.1:17801,localaddr=127.0.0.1 \
 -device virtio-net-pci,netdev=lan,mac=52:54:00:78:ff:02 \
 -qmp "unix:$D/qmp.sock,server=on,wait=off" -serial "file:$D/serial.log" -display none -daemonize -pidfile "$D/pid"
