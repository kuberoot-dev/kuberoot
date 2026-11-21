#!/bin/sh
set -eu
cd /data
profile=${1:?baseline or emulator}
case "$profile" in
 baseline) args=kuberoot.timescale=1 ;;
 emulator) args=kuberoot.profile=emulator ;;
 *) exit 1 ;;
esac
# CPU quota in pod.yaml slows wall-clock execution, not the guest's timers.
# Both runs use exactly the same machine, image, quota and 1024 MiB RAM.
exec qemu-system-riscv64 -machine virt -accel tcg -cpu rv64,v=false,h=false,sv39=on -smp 1 -m 1024 -nographic -bios default \
 -kernel vmlinuz.efi -initrd initrd.cpio \
 -append "console=ttyS0 earlycon=sbi kuberoot.root=PARTLABEL=KUBEROOT-MEDIA:/kuberoot/rootfs.squashfs kuberoot.verbose $args" \
 -drive file=media.img,format=raw,if=none,id=media,readonly=on -device virtio-blk-device,drive=media \
 -netdev user,id=net,hostfwd=tcp:0.0.0.0:50000-:50000,hostfwd=tcp:0.0.0.0:6443-:6443 -device virtio-net-device,netdev=net \
 -device virtio-rng-device
