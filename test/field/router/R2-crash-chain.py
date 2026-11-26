#!/usr/bin/env python3
import json,subprocess,time,urllib.request
from pathlib import Path
p=Path('/work/field/raw/trial-field.2-noforward.summary.json');deadline=time.monotonic()+1200
while time.monotonic()<deadline:
 if p.exists():
  r=json.loads(p.read_text())
  if r.get('timeout') or r.get('unsafe_promotion'):raise SystemExit('previous fault failed: preserve state')
  if r.get('booted',{}).get('release')=='0.1.1-router.field.2':
   try:
    with urllib.request.urlopen('http://127.0.0.1:8090/',timeout=2) as f:
     if json.load(f)['ok']:break
   except Exception:pass
 time.sleep(3)
else:raise SystemExit('previous trial not recovered')
Path('/work/field/R2-fixed-crash.sh').write_text("#!/bin/bash\nsource /work/field/lib.sh\nshow 'date -u +%FT%TZ'\nshow 'python3 /work/field/R2-upgrade-trial.py 0.1.1-router.field.2-crash --expect-reject --timeout 600'\nshow 'date -u +%FT%TZ'\n")
subprocess.run(['env','DEFER_RENDER=1','bash','/work/field/capture.sh','t-R2-kuberoot-crash-rollback','/work/field/R2-fixed-crash.sh'],check=True)
