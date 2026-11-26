#!/bin/bash
set -euo pipefail
for mode in crash noforward console; do
 bash /work/field/build-fault-bundle.sh "$mode" /work/field-fixes/root "0.1.1-router.field.2-$mode" > "/work/field/raw/build-field-2-$mode.log" 2>&1
done
touch /work/field/render.pause
python3 /work/field/forwarding.py linux-fabric direct-low-udp --direct --mode udp64 --udp-rate 1M --seconds 15 --repeats 2 --keep-going > /work/field/raw/R-B2-direct-low-udp.log 2>&1
bash /work/field/debian-forwarding-profiles.sh > /work/field/raw/R-B2-debian-all.log 2>&1
printf '0\n' > /work/field/raw/R-B2-debian-all.exit
rm /work/field/render.pause
