#!/bin/bash
# Render a scene recorded with DEFER_RENDER=1; speeds playback up when the
# retimed cast is longer than the 60 s budget.
set -euo pipefail
name=$1
cast=/work/field/casts/$name.retimed.cast
[[ $(cat /work/field/casts/$name.exit) == 0 ]] || { echo "$name failed; not rendered"; exit 1; }
len=$(python3 -c "import json;l=[json.loads(x) for x in open('$cast')][1:];print(l[-1][0] if l else 0)")
speed=$(python3 -c "print(max(1.0, round($len/55, 2)))")
agg --fps-cap "${AGG_FPS:-15}" --font-size "${AGG_FONT:-16}" --theme dracula --speed "$speed" "$cast" "/work/field/img/$name.gif"
python3 - "$name" <<'PY'
import sys
from PIL import Image
from pathlib import Path
p=Path('/work/field/img')/(sys.argv[1]+'.gif'); im=Image.open(p); d=0
for i in range(im.n_frames):
 im.seek(i); d+=im.info.get('duration',0)
im.convert('RGB').save(p.with_suffix('.png'))
print({'file':p.name,'seconds':d/1000,'bytes':p.stat().st_size})
PY
