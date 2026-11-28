#!/bin/bash
# Lay out r2's system disk the way the installer does (efi, root-a, root-b,
# state) from a build's rootfs, and boot it with the build's kernel and initrd
# directly. The launch arguments go to /work/vms/r2/launch-directboot.json, the
# shape r11-prepare.sh and r11-test-build.py derive r11 from.
# usage: r2-directboot.sh <build out dir, e.g. /work/alpine/out>
set -euo pipefail
out=${1:?build out dir}/amd64; d=/work/vms/r2
mkdir -p $d/directboot; cd $d
[ -e pid ] && { echo "r2 is running" >&2; exit 1; }
cp $out/vmlinuz.efi $out/initrd.cpio directboot/
cp $out/VERSION directboot/VERSION
rm -f sys.raw; truncate -s 12G sys.raw
sgdisk -o -n 1:2048:+512M -t 1:ef00 -c 1:kuberoot-efi -n 2:0:+1024M -c 2:kuberoot-root-a -n 3:0:+1024M -c 3:kuberoot-root-b -n 4:0:0 -c 4:kuberoot-state sys.raw >/dev/null
first() { sgdisk -i $1 sys.raw | awk '/First sector/{print $3}'; }
last() { sgdisk -i $1 sys.raw | awk '/Last sector/{print $3}'; }
mkfs.vfat -F 32 -n KUBEROOTEFI --offset $(first 1) sys.raw $(( ($(last 1) - $(first 1) + 1) / 2 )) >/dev/null
dd if=$out/rootfs.squashfs of=sys.raw bs=512 seek=$(first 2) conv=notrunc status=none
mkfs.ext4 -q -F -L kuberoot-state -E offset=$(( $(first 4) * 512 )) sys.raw $(( ($(last 4) - $(first 4) + 1) / 2 ))k
qemu-img convert -O qcow2 sys.raw sys.qcow2 && rm -f sys.raw
python3 - <<'PY'
import json, subprocess
d = '/work/vms/r2'
argv = ['qemu-system-x86_64', '-name', 'r2', '-enable-kvm', '-cpu', 'host', '-smp', '2', '-m', '2048', '-machine', 'q35',
        '-kernel', f'{d}/directboot/vmlinuz.efi', '-initrd', f'{d}/directboot/initrd.cpio',
        '-append', 'kuberoot.root=PARTLABEL=kuberoot-root-a quiet panic=10 console=ttyS0,115200 kuberoot.ip=eth2:10.255.0.2/24 kuberoot.dev',
        '-netdev', 'user,id=wan,hostfwd=tcp:127.0.0.1:5020-:50000,hostfwd=tcp:127.0.0.1:6423-:6443', '-device', 'virtio-net-pci,netdev=wan,mac=52:54:00:00:02:01',
        '-netdev', 'socket,id=lan,mcast=230.0.0.2:12002,localaddr=127.0.0.1', '-device', 'virtio-net-pci,netdev=lan,mac=52:54:00:01:02:01',
        '-netdev', 'socket,id=transit,mcast=230.0.0.3:12003,localaddr=127.0.0.1', '-device', 'virtio-net-pci,netdev=transit,mac=52:54:00:02:02:01',
        '-drive', f'file={d}/sys.qcow2,format=qcow2,if=none,id=sysdisk', '-device', 'virtio-blk-pci,drive=sysdisk,addr=0x5',
        '-netdev', 'socket,id=glan,mcast=230.31.0.31:13031,localaddr=127.0.0.1', '-device', 'virtio-net-pci,romfile=,netdev=glan,mac=52:54:00:30:00:12,addr=0x6',
        '-netdev', 'socket,id=gwan,mcast=230.31.0.32:13032,localaddr=127.0.0.1', '-device', 'virtio-net-pci,romfile=,netdev=gwan,mac=52:54:00:30:00:11,addr=0x7',
        '-chardev', f'socket,id=ser,path={d}/serial.sock,server=on,wait=off,logfile={d}/console.log,logappend=on', '-serial', 'chardev:ser',
        '-qmp', f'unix:{d}/qmp.sock,server=on,wait=off', '-display', 'none', '-daemonize', '-pidfile', f'{d}/pid']
json.dump({'argv': argv}, open(f'{d}/launch-directboot.json', 'w'), indent=1)
subprocess.run(argv, check=True)
PY
echo "r2 started: $(cat directboot/VERSION)"
