#!/usr/bin/env python3
"""One-hour observed idle I/O and guest/service memory; never invent MemAvailable."""
import json,os,subprocess,sys,time
from pathlib import Path
name=sys.argv[1]; seconds=int(sys.argv[2]) if len(sys.argv)>2 else 3600
if name not in ('r1','r2'):raise SystemExit('supported: r1/r2')
base=Path('/work/field/raw');base.mkdir(parents=True,exist_ok=True)
node='5010' if name=='r1' else '5020'
env=dict(os.environ,KUBECONFIG='/work/trust/admin.kubeconfig')
start=time.monotonic(); wall=time.time()
with (base/f'R-B1-{name}-idle.jsonl').open('a',buffering=1) as out:
 while True:
  row={'wall_utc':time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime()),'elapsed_s':time.monotonic()-start,'vm':name}
  for resource in ('osconfigs','nodeservices'):
   p=subprocess.run(['kubectl','--server',f'https://127.0.0.1:{node}','--request-timeout=5s','get',resource,'-o','json'],env=env,capture_output=True,text=True)
   row[resource]=json.loads(p.stdout) if p.returncode==0 else {'error':p.stderr}
  p=subprocess.run(['python3','/work/field/qmp.py',name,'query-blockstats'],capture_output=True,text=True)
  row['blockstats']=json.loads(p.stdout) if p.returncode==0 else {'error':p.stderr}
  pid=int((Path('/work/vms')/name/'pid').read_text())
  row['qemu_status']={line.split(':')[0]:line.split(':',1)[1].strip() for line in Path(f'/proc/{pid}/status').read_text().splitlines() if line.startswith(('VmRSS:','VmHWM:','Threads:'))}
  out.write(json.dumps(row)+'\n')
  if time.monotonic()-start>=seconds:break
  time.sleep(min(30,max(0,seconds-(time.monotonic()-start))))
print(json.dumps({'complete':True,'vm':name,'elapsed_s':time.monotonic()-start,'path':str(base/f'R-B1-{name}-idle.jsonl')}))
subprocess.run(['python3','/work/field/summarize-idle.py',str(base/f'R-B1-{name}-idle.jsonl')],check=True)
subprocess.run(['python3','/work/field/plot-idle.py',str(base/f'R-B1-{name}-idle.jsonl'),f'/work/field/img/chart-R-B1-{name}-idle.png'],check=True)
