#!/bin/bash
# Render an already completed, genuine asciinema recording inside the pod.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
name=${1:?existing capture name};[[ $name =~ ^[A-Za-z0-9_.-]+$ ]] || exit 2
out=$FIELD_ROOT/captures
python3 /work/scripts/render.py "$out/$name.cast" > "$out/$name.timing.json"
python3 "$FIELD_SCRIPTS/bound-cast.py" "$out/$name.retimed.cast" > "$out/$name.speed.json"
hold=$(python3 -c 'import json,sys;print(max(5,15.1-json.load(open(sys.argv[1]))["playback_seconds"]))' "$out/$name.timing.json")
agg -q --font-size 16 --theme monokai --last-frame-duration "$hold" "$out/$name.retimed.cast" "$out/$name.gif"
python3 "$FIELD_SCRIPTS/terminal-png.py" "$out/$name.txt" "$out/$name.png"
python3 "$FIELD_SCRIPTS/verify-capture.py" "$out/$name" > "$out/$name-verification.json"
