#!/bin/bash
set -euo pipefail
source /work/field/lib.sh
show 'date -u; python3 /work/field/qmp.py debian query-status'
tail -n 0 -f /work/vms/debian/console.log & follower=$!
trap 'kill "$follower" 2>/dev/null || true' EXIT
show 'date -u; python3 /work/field/boot-debian.py --trials 1; date -u'
kill "$follower" 2>/dev/null || true
wait "$follower" 2>/dev/null || true
show 'cat /work/field/raw/R-B1-debian-boot-01.json | python3 -c '\''import json,sys; x=json.load(sys.stdin); x.pop("samples"); print(json.dumps(x,indent=2))'\'''
