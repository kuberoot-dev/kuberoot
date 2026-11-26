#!/usr/bin/env python3
import json,subprocess,time,urllib.request
from pathlib import Path
p=Path('/work/field/raw/trial-field.3-crash.summary.json');deadline=time.monotonic()+900
while time.monotonic()<deadline:
 if p.exists():
  r=json.loads(p.read_text())
  if r.get('timeout') or r.get('unsafe_promotion'):raise SystemExit('crash trial failed; preserve state')
  if r.get('booted',{}).get('release')=='0.1.1-router.field.3':
   try:
    with urllib.request.urlopen('http://127.0.0.1:8090/',timeout=2) as f:
     if json.load(f)['ok']:break
   except Exception:pass
 time.sleep(3)
else:raise SystemExit('crash trial did not recover')
for trial in range(1,31):
 phase='write' if trial%2 else 'selection'
 if trial<=2:
  scene=Path(f'/work/field/R2-power-{phase}.sh')
  scene.write_text("#!/bin/bash\nsource /work/field/lib.sh\nshow 'date -u +%FT%TZ'\nshow 'python3 /work/field/R2-power-cut.py "+str(trial)+" "+phase+"'\nshow 'date -u +%FT%TZ'\n")
  cmd=['env','DEFER_RENDER=1','bash','/work/field/capture.sh','t-R2-kuberoot-power-'+phase,str(scene)]
 else:cmd=['python3','/work/field/R2-power-cut.py',str(trial),phase]
 proc=subprocess.run(cmd)
 if proc.returncode:raise SystemExit(f'trial {trial} failed; retain evidence for a second approach')
Path('/work/field/raw/R2-power-cut-chain.exit').write_text('0\n')
