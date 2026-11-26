#!/bin/bash
# Record actual commands/output. Renderer runs only inside the workshop pod.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
name=${1:?capture-name}; script=${2:?scene-script}
[[ $name =~ ^[A-Za-z0-9_.-]+$ ]] || exit 2
if [[ $name == t-E2-* ]];then
 python3 "$FIELD_SCRIPTS/prepare-run-capacity.py"
 python3 "$FIELD_SCRIPTS/retire-completed-e1-disks.py"
fi
out=$FIELD_ROOT/captures
mkdir -p "$out"
export COLUMNS=110 LINES=30 TERM=xterm-256color
asciinema rec --cols 110 --rows 30 -q -c "bash '$script'; result=\$?; printf '%s\\n' \"\$result\" > '$out/$name.exit'" "$out/$name.cast"
python3 /work/scripts/render.py "$out/$name.cast" > "$out/$name.timing.json"
python3 "$FIELD_SCRIPTS/bound-cast.py" "$out/$name.retimed.cast" > "$out/$name.speed.json"
hold=$(python3 -c 'import json,sys; print(max(5,15.1-json.load(open(sys.argv[1]))["playback_seconds"]))' "$out/$name.timing.json")
agg -q --font-size 16 --theme monokai --last-frame-duration "$hold" "$out/$name.retimed.cast" "$out/$name.gif"
python3 "$FIELD_SCRIPTS/terminal-png.py" "$out/$name.txt" "$out/$name.png"
test "$(stat -c %s "$out/$name.gif")" -lt 8388608

exit "$(cat "$out/$name.exit")"
