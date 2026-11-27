#!/usr/bin/env python3
"""Guest-side durable native nft configuration updates at an offered ten per second."""
import json,os,subprocess,time
from pathlib import Path
root=Path('/var/lib/field-config');root.mkdir(exist_ok=True)
config=root/'load.nft';start=time.monotonic();next_tick=start;ok=0;errors=0;latencies=[]
subprocess.run(['nft','add','table','inet','fieldwrite'],check=True)
while time.monotonic()-start<3600:
 t=time.monotonic();sequence=ok+errors+1
 text=f'delete table inet fieldwrite\ntable inet fieldwrite {{\n comment "sequence-{sequence}"\n}}\n'
 try:
  with (root/'load.tmp').open('w') as f:f.write(text);f.flush();os.fsync(f.fileno())
  os.replace(root/'load.tmp',config);fd=os.open(root,os.O_DIRECTORY);os.fsync(fd);os.close(fd)
  p=subprocess.run(['nft','-f',str(config)],capture_output=True,text=True)
  if p.returncode:raise RuntimeError(p.stderr)
  ok+=1
 except Exception as e:errors+=1;print(json.dumps({'at':time.time(),'sequence':sequence,'error':str(e)}),flush=True)
 latencies.append(time.monotonic()-t)
 if sequence%100==0:
  print(json.dumps({'at':time.time(),'elapsed_s':time.monotonic()-start,'accepted':ok,'errors':errors,'last100_max_s':max(latencies),'last100_mean_s':sum(latencies)/len(latencies)}),flush=True);latencies=[]
 next_tick+=.1;time.sleep(max(0,next_tick-time.monotonic()))
print(json.dumps({'phase':'complete','elapsed_s':time.monotonic()-start,'accepted':ok,'errors':errors,'actual_updates_s':ok/(time.monotonic()-start)}),flush=True)
