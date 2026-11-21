#!/bin/sh
set -eu
cd /data
ram=${1:?RAM MiB}
# Each invocation runs a fresh live node, without persistent state from a
# previous memory size. The media is immutable; /var is guest tmpfs.
exec qemu-system-riscv64 -machine virt -accel tcg -cpu rv64,v=false,h=false,sv39=on -smp 4 -m "$ram" -nographic -bios default \
 -kernel vmlinuz.efi -initrd initrd.cpio \
 -append 'console=ttyS0 earlycon=sbi kuberoot.root=PARTLABEL=KUBEROOT-MEDIA:/kuberoot/rootfs.squashfs kuberoot.verbose' \
 -drive file=media.img,format=raw,if=none,id=media,readonly=on -device virtio-blk-device,drive=media \
 -netdev user,id=net,hostfwd=tcp:0.0.0.0:50000-:50000,hostfwd=tcp:0.0.0.0:6443-:6443 -device virtio-net-device,netdev=net \
 -device virtio-rng-device
