#!/usr/bin/env python3
import json,pathlib,sys
from PIL import Image
stem=sys.argv[1]
if stem.endswith(('.cast','.gif')):stem=stem.rsplit('.',1)[0]
cast=pathlib.Path(stem+'.cast');gif=pathlib.Path(stem+'.gif')
h=json.loads(cast.read_text().splitlines()[0]);im=Image.open(gif);duration=0
for i in range(im.n_frames):im.seek(i);duration+=im.info.get('duration',0)/1000
r={'file':str(gif),'bytes':gif.stat().st_size,'columns':h['width'],'rows':h['height'],'duration_seconds':duration,'pixels':im.size,'frames':im.n_frames}
print(json.dumps(r,indent=2));assert h['width']==110 and h['height']==30;assert 15<=duration<=60,r;assert r['bytes']<8*1024**2
