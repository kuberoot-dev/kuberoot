#!/usr/bin/env python3
import hashlib,json
from pathlib import Path
from PIL import Image
root=Path('/work/field');items=[]
for p in sorted((root/'img').glob('*.gif')):
 im=Image.open(p);ms=0
 for i in range(im.n_frames):im.seek(i);ms+=im.info.get('duration',0)
 ep=root/'casts'/(p.stem+'.exit');status=int(ep.read_text()) if ep.exists() else None
 item={'name':p.name,'bytes':p.stat().st_size,'seconds':ms/1000,'size':im.size,'scene_exit':status,
       'sha256':hashlib.sha256(p.read_bytes()).hexdigest(),'budget_ok':15<=ms/1000<=60 and p.stat().st_size<8*1024*1024,
       'raw':(root/'raw'/(p.stem+'.txt')).exists()}
 item['publishable']=item['budget_ok'] and item['raw'] and (status == 0 or (status is None and p.stem in ('t-R-B1-kuberoot-memory','t-R-B1-kuberoot-status','t-R-B1-openwrt-boot-console')))
 items.append(item)
(root/'raw'/'phase2-artifact-validation.json').write_text(json.dumps(items,indent=2)+'\n')
print(json.dumps(items,indent=2))
