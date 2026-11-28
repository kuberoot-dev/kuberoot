#!/bin/bash
# The offline Linux tools base (linux-tools.qcow2) the client, gateway and sink
# endpoints of fabric-vm.py boot from.
set -euo pipefail
cd /work/field/images
[ -f /work/field/private/id_ed25519 ] || ssh-keygen -q -t ed25519 -N '' -f /work/field/private/id_ed25519
[ -s debian-12-genericcloud-amd64.qcow2 ] || curl -fsSL --retry 3 https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-genericcloud-amd64.qcow2 -o debian-12-genericcloud-amd64.qcow2
sha256sum debian-12-genericcloud-amd64.qcow2 > /work/field/raw/debian-cloud.sha256
d=/work/tmp/toolsbase; mkdir -p $d
qemu-img create -q -f qcow2 -F qcow2 -b /work/field/images/debian-12-genericcloud-amd64.qcow2 $d/sys.qcow2 8G
cat > $d/user-data <<UD
#cloud-config
package_update: true
packages: [fping, iperf3, tcpdump, python3-scapy, netcat-openbsd, nftables, bird2, iproute2, python3, ethtool, conntrack, dnsutils]
runcmd:
 - [systemctl, disable, bird, nftables]
 - [cloud-init, clean, --logs]
 - [poweroff]
UD
printf 'instance-id: toolsbase\nlocal-hostname: toolsbase\n' > $d/meta-data
cloud-localds $d/seed.img $d/user-data $d/meta-data
timeout 1500 qemu-system-x86_64 -enable-kvm -cpu host -smp 2 -m 1024 -drive file=$d/sys.qcow2,if=virtio -drive file=$d/seed.img,if=virtio,format=raw,readonly=on -netdev user,id=n -device virtio-net-pci,netdev=n -display none -serial file:$d/console.log
qemu-img convert -O qcow2 $d/sys.qcow2 /work/field/images/linux-tools.qcow2
qemu-img info /work/field/images/linux-tools.qcow2 | head -4
echo TOOLS-OK
