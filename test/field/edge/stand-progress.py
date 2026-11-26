#!/usr/bin/env python3
"""Read-only progress of the owned field pod; no credentials or guest mutation."""
import datetime,json,pathlib,shutil
root=pathlib.Path('/work/field-edge');active=[]
for p in pathlib.Path('/proc').glob('[0-9]*/cmdline'):
 try:args=p.read_bytes().decode().split('\0')
 except (OSError,UnicodeError):continue
 if not args[0].endswith('qemu-system-x86_64') or '-name' not in args:continue
 name=args[args.index('-name')+1]
 if name=='edge-field-gateway':continue
 qmp=args[args.index('-qmp')+1].split(',')[0].removeprefix('unix:');d=pathlib.Path(qmp).parent
 r={'pid':int(p.parent.name),'name':name,'directory':str(d)}
 f=d/'observations.jsonl'
 if f.is_file():
  lines=f.read_text().splitlines()
  if lines:
   last=json.loads(lines[-1]);r.update({k:last[k] for k in ['utc','elapsed_seconds','booted','fault_verified','error','pvc_free_bytes'] if k in last})
 active.append(r)
rows=[json.loads(x) for x in (root/'results.jsonl').read_text().splitlines()]
valid=[r for r in rows if r.get('scenario_id')=='E2' and r.get('measurement_protocol')=='upgrade-client-disconnect-v2' and r.get('status') in ['passed','failed']]
counts={}
for r in valid:counts.setdefault(r['product'],{}).setdefault(r['fault_phase'],set()).add(r['repetition'])
counts={product:{phase:len(reps) for phase,reps in phases.items()} for product,phases in counts.items()}
last=rows[-1]
print(json.dumps({'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'pvc_free_bytes':shutil.disk_usage(root).free,'active_dut':active,'E2_completed_repetitions':counts,'last_published':{k:last.get(k) for k in ['scenario_id','product','fault_phase','repetition','status','time_utc']}},indent=2))
