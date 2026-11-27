#!/bin/bash
# Build r11: a copy of r2 whose state partition lives on a separate, small disk
# served over NBD, so the state store can be filled or failed without touching
# r2 or the pod's volume. Stops r2 for the copy and starts it again.
set -euo pipefail
d=/work/vms/r11; mkdir -p "$d"; cd "$d"
headroom_mib=${HEADROOM_MIB:-192}
# QEMU removes its pidfile on exit; kill -0 is no test here, an unreaped
# QEMU stays a zombie in this pod.
if [[ -e /work/vms/r2/pid ]]; then
 python3 /work/field/qmp.py r2 system_powerdown >/dev/null
 for _ in $(seq 120); do [[ -e /work/vms/r2/pid ]] || break; sleep 1; done
 [[ -e /work/vms/r2/pid ]] && { python3 /work/field/qmp.py r2 quit >/dev/null || true; sleep 2; }
fi
qemu-img convert -O raw /work/vms/r2/sys.qcow2 "$d/sys.raw"
python3 - <<'PY'
import json, subprocess
argv = json.load(open('/work/vms/r2/launch-directboot.json'))['argv']
subprocess.run(argv, check=True)
PY
echo "r2 restarted"
sgdisk -p sys.raw | tee sgdisk-before.txt
n=$(sgdisk -p sys.raw | awk '$7=="kuberoot-state"{print $1}')
start=$(sgdisk -i "$n" sys.raw | awk '/First sector/{print $3}')
end=$(sgdisk -i "$n" sys.raw | awk '/Last sector/{print $3}')
dd if=sys.raw of=state-src.ext4 bs=512 skip="$start" count=$((end - start + 1)) conv=sparse status=none
sgdisk -c "$n":kuberoot-state-r2 sys.raw >/dev/null
e2fsck -fy state-src.ext4 >e2fsck-src.txt 2>&1 || true
resize2fs -M state-src.ext4 >resize-min.txt 2>&1
blocks=$(dumpe2fs -h state-src.ext4 2>/dev/null | awk -F: '/^Block count/{gsub(/ /,"",$2);print $2}')
bsize=$(dumpe2fs -h state-src.ext4 2>/dev/null | awk -F: '/^Block size/{gsub(/ /,"",$2);print $2}')
fs_mib=$(( blocks * bsize / 1048576 + 1 ))
disk_mib=$(( fs_mib + headroom_mib + 2 ))
rm -f state.img; truncate -s "${disk_mib}M" state.img
sgdisk -n 1:2048:0 -c 1:kuberoot-state state.img >/dev/null
pstart=$(sgdisk -i 1 state.img | awk '/First sector/{print $3}'); pend=$(sgdisk -i 1 state.img | awk '/Last sector/{print $3}')
# Grow a copy of the filesystem to the partition, then write it into the
# fresh (all-zero) image; conv=sparse is only safe into zeroes.
cp --sparse=always state-src.ext4 state-part.ext4
truncate -s $(( (pend - pstart + 1) * 512 )) state-part.ext4
resize2fs state-part.ext4 >resize-grow.txt 2>&1
e2fsck -fn state-part.ext4 >e2fsck-part.txt 2>&1
dd if=state-part.ext4 of=state.img bs=512 seek="$pstart" conv=notrunc,sparse status=none
rm -f state-part.ext4
echo "{\"state_fs_min_mib\": $fs_mib, \"state_disk_mib\": $disk_mib, \"partition_first_sector\": $pstart}" | tee layout.json
sed 's/127.0.0.1:6423/127.0.0.1:6424/' /work/cluster-r2.conf > /work/cluster-r11.conf
