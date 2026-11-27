#!/bin/bash
# Capture the actual script, retain wall-clock output, shorten only idle gaps.
set -euo pipefail
name=${1:?scene name}; script=${2:?script}
[[ $name =~ ^t-[a-zA-Z0-9_-]+$ && -f $script ]] || exit 2
export TERM=xterm-256color COLUMNS=110 LINES=30
mkdir -p /work/field/{casts,img,raw}
asciinema rec --overwrite --cols 110 --rows 30 -q -c "bash '$script'; result=\$?; printf '%s\n' \"\$result\" > '/work/field/casts/$name.exit'; exit \"\$result\"" "/work/field/casts/$name.cast" </dev/null
python3 /work/retime.py "/work/field/casts/$name.cast" "/work/field/casts/$name.retimed.cast" "/work/field/raw/$name.txt" > "/work/field/casts/$name.timing.json"
[[ $(cat "/work/field/casts/$name.exit") == 0 ]] || { echo "scene failed; retained raw/cast, do not publish" >&2; exit 1; }
if [[ ${DEFER_RENDER:-0} == 1 ]]; then exit 0; fi
while [[ -e /work/field/render.pause ]]; do sleep 5; done
agg --fps-cap "${AGG_FPS:-15}" --font-size 16 --theme dracula "/work/field/casts/$name.retimed.cast" "/work/field/img/$name.gif"
python3 - "$name" <<'PY'
import sys
from PIL import Image
from pathlib import Path
p=Path('/work/field/img')/(sys.argv[1]+'.gif')
im=Image.open(p); duration=0
for i in range(im.n_frames):
 im.seek(i);duration+=im.info.get('duration',0)
im.convert('RGB').save(p.with_suffix('.png'))
print({'file':p.name,'seconds':duration/1000,'bytes':p.stat().st_size})
if p.stat().st_size>8*1024*1024:raise SystemExit('GIF over size budget')
if not 15<=duration/1000<=60:raise SystemExit('GIF outside duration budget; split scene')
PY
