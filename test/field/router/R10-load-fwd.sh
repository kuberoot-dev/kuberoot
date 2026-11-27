#!/bin/bash
# API load on r1 while the c1 -> r1 -> r2 -> c2 forwarding observer is polled twice a second.
source /work/field/lib.sh
mv /work/field/raw/R10-kuberoot-load.summary.json /work/field/raw/R10-kuberoot-load-first.summary.json 2>/dev/null
( end=$((SECONDS+70)); while [ $SECONDS -lt $end ]; do printf '%s %s\n' "$(date +%s.%N)" "$(curl -s -o /dev/null -w %{http_code} -m 2 http://127.0.0.1:8090/)"; sleep 0.5; done ) > /work/field/raw/R10-kuberoot-load-fwd-observer.txt &
show 'python3 /work/field/R10-api-load.py'
wait
show "awk '{print \$2}' /work/field/raw/R10-kuberoot-load-fwd-observer.txt | sort | uniq -c"
