#!/bin/bash
set -uo pipefail
cd /work/field
python3 openwrt-fabric-setup.py > raw/openwrt-reinstall2-setup.log 2>&1; echo "setup exit $?"
for f in images/openwrt-pkgs/*.ipk; do /work/field/openwrt-command "cat > /tmp/$(basename "$f")" < "$f"; done
/work/field/openwrt-command "opkg install /tmp/terminfo_*.ipk /tmp/libncurses6_*.ipk /tmp/libreadline8_*.ipk /tmp/bird2_*.ipk /tmp/bird2c_*.ipk 2>&1 | tail -2; bird --version"
printf "#!/bin/bash\nsource /work/field/lib.sh\nshow %q\n" "python3 /work/field/comp.py openwrt r7 10000,100000" > scene-t-R7-openwrt-small2.sh
DEFER_RENDER=1 bash capture.sh t-R7-openwrt-small2 scene-t-R7-openwrt-small2.sh > raw/R7-openwrt-small2.capture.log 2>&1; echo "r7 exit $(cat casts/t-R7-openwrt-small2.exit)"
echo openwrt-r7-done
