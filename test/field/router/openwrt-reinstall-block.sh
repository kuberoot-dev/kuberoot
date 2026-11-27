#!/bin/bash
# OpenWrt block: reinstall from the pinned image (the broken VM is kept as
# evidence under another name), install bird2 from the release feed files,
# then rerun the rows that were blocked.
set -uo pipefail
cd /work/field
for other in r11 vyos debian; do [[ -e /work/vms/$other/pid ]] && { echo "$other is running"; exit 1; }; done
img=images/openwrt-24.10.8.img.gz
want=$(awk '/generic-squashfs-combined-efi.img.gz$/{print $1}' images/openwrt.sha256sums)
have=$(sha256sum "$img" | awk '{print $1}')
echo "image sha256 $have (release list: $want)"; [[ $want == "$have" ]] || { echo "image digest mismatch"; exit 1; }
if [[ -d /work/vms/openwrt && ! -d /work/vms/openwrt-broken-after-enospc ]]; then
 mv /work/vms/openwrt /work/vms/openwrt-broken-after-enospc
fi
python3 openwrt-fabric-setup.py > raw/openwrt-reinstall-setup.log 2>&1; echo "setup exit $?"
for f in images/openwrt-pkgs/*.ipk; do /work/field/openwrt-command "cat > /tmp/$(basename "$f")" < "$f"; done
/work/field/openwrt-command 'opkg install /tmp/terminfo_*.ipk /tmp/libncurses6_*.ipk /tmp/libreadline8_*.ipk /tmp/bird2_*.ipk /tmp/bird2c_*.ipk 2>&1 | tail -5; bird --version' | tee raw/openwrt-reinstall-bird2.txt
/work/field/guest-command client 'ping -c 3 -W 1 198.19.0.1 | tail -1' | tee raw/openwrt-reinstall-forwarding.txt
scene() { local name=$1; shift; printf "#!/bin/bash\nsource /work/field/lib.sh\nshow %q\n" "$*" > /work/field/scene-$name.sh; DEFER_RENDER=1 bash /work/field/capture.sh "$name" /work/field/scene-$name.sh || echo "FAILED $name"; echo "DONE $name $(date -u +%T)"; }
scene t-R-B1-openwrt-boot "python3 /work/field/comp.py openwrt boot 3"
for prof in routing nat stateful; do
 python3 comp.py openwrt b2prof $prof > raw/R-B2-openwrt-$prof-apply.log 2>&1
 scene t-R-B2-openwrt-$prof "python3 /work/field/forwarding.py openwrt $prof --repeats 3 --seconds 30 --keep-going"
done
scene t-R5-openwrt-flows "python3 /work/field/comp.py openwrt r5"
scene t-R6-openwrt-scale "python3 /work/field/comp.py openwrt r6 100 1000 10000"
scene t-R7-openwrt-small "python3 /work/field/comp.py openwrt r7 10000,100000"
echo openwrt-reruns-done
# R3, R8, R9 need bird2 and, for R9, a fourth NIC on the transit segment; R2 last
# (sysupgrade drops installed packages).
python3 qmp.py openwrt system_powerdown >/dev/null; for i in $(seq 90); do [[ -e /work/vms/openwrt/pid ]] || break; sleep 1; done
[[ -e /work/vms/openwrt/pid ]] && { python3 qmp.py openwrt quit >/dev/null; sleep 2; }
FIELD_EXTRA_NET="-netdev socket,id=transit,mcast=230.0.0.3:12003,localaddr=127.0.0.1 -device virtio-net-pci,romfile=,netdev=transit,mac=52:54:00:30:00:04" bash fabric-competitor.sh openwrt
for i in $(seq 100); do /work/field/openwrt-command true 2>/dev/null && break; sleep 3; done
/work/field/openwrt-command 'ip -br link' | tee raw/openwrt-rows-links.txt
tdev=$(/work/field/openwrt-command 'ip -br link' | awk '/52:54:00:30:00:04/{print $1}' | sed 's/@.*//')
echo "transit device: $tdev"
for r in r3 r8 r9 r2; do
 ENVLINE="export OPENWRT_TRANSIT_DEV=$tdev"
 printf "#!/bin/bash\nsource /work/field/lib.sh\n%s\nshow %q\n" "$ENVLINE" "python3 /work/field/openwrt-rows.py $r" > scene-t-${r^^}-openwrt-rows.sh
 DEFER_RENDER=1 bash capture.sh t-${r^^}-openwrt-rows scene-t-${r^^}-openwrt-rows.sh > raw/${r^^}-openwrt-rows.capture.log 2>&1
 echo "$r exit $(cat casts/t-${r^^}-openwrt-rows.exit 2>/dev/null)"
done
echo openwrt-rows-done
