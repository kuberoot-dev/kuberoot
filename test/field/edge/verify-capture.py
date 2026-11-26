#!/usr/bin/env python3
import json,pathlib,sys
from PIL import Image
p=pathlib.Path(sys.argv[1]);h=json.loads(p.with_suffix('.cast').read_text().splitlines()[0]);im=Image.open(p.with_suffix('.gif'));duration=0
for i in range(im.n_frames):im.seek(i);duration+=im.info.get('duration',0)/1000
r={'file':str(p.with_suffix('.gif')),'bytes':p.with_suffix('.gif').stat().st_size,'columns':h['width'],'rows':h['height'],'duration_seconds':duration,'pixels':im.size,'frames':im.n_frames}
print(json.dumps(r,indent=2));assert h['width']==110 and h['height']==30;assert 15<=duration<=60,r;assert r['bytes']<8*1024**2
