#!/usr/bin/env python3
"""Render real recorded events; retain full casts and record time compression."""
import json,subprocess,sys
from pathlib import Path
from PIL import Image
base=Path('/work/field'); names=sys.argv[1:]
for name in names:
 src=base/'casts'/(name+'.cast');dst=base/'casts'/(name+'.publication.cast');out=base/'img'/(name+'.gif')
 if out.exists():continue
 events=[json.loads(x) for x in src.read_text().splitlines()];header=events[0];header['width']=110;header['height']=30
 last=t=0;timed=[]
 for row in events[1:]:
  t+=min(max(0,row[0]-last),.5);last=row[0];timed.append([t,*row[1:]])
 scale=min(1,55/max(t,.001))
 with dst.open('w') as f:
  f.write(json.dumps(header)+'\n')
  for row in timed:row[0]=round(row[0]*scale,6);f.write(json.dumps(row)+'\n')
  if t*scale<16:f.write(json.dumps([16,'o',''])+'\n')
 metadata={'source':src.name,'source_duration_s':last,'idle_gap_cap_s':.5,'additional_time_scale':scale,'all_recorded_output_events_retained':True}
 (base/'casts'/(name+'.publication.json')).write_text(json.dumps(metadata,indent=2)+'\n')
 subprocess.run(['agg','--fps-cap','3','--font-size','16','--theme','dracula',str(dst),str(out)],check=True)
 im=Image.open(out);duration=0
 for i in range(im.n_frames):im.seek(i);duration+=im.info.get('duration',0)
 im.convert('RGB').save(out.with_suffix('.png'));metadata.update(bytes=out.stat().st_size,duration_s=duration/1000,frames=im.n_frames)
 print(json.dumps({'name':name,**metadata}),flush=True)
 if out.stat().st_size>8*1024*1024:raise SystemExit('GIF exceeds limit; rerender at lower fps or split')
