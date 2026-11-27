#!/usr/bin/env python3
"""Retest with matching root/initramfs kinit; keep earlier mixed-build evidence."""
import json,subprocess,time,os
from pathlib import Path
base=Path('/work/field');env=dict(os.environ,KUBECONFIG='/work/trust/admin.kubeconfig')
def healthy():
 p=subprocess.run(['kubectl','--server','https://127.0.0.1:5010','--request-timeout=3s','get','bootentries','-o','json'],env=env,capture_output=True,text=True)
 if p.returncode:return False
 return any(e['status'].get('booted') and e['status'].get('state')=='Good' for e in json.loads(p.stdout)['items'])
def capture(name,command):
 script=base/(name+'.sh');script.write_text("#!/bin/bash\nsource /work/field/lib.sh\nshow 'date -u +%FT%TZ'\nshow '"+command+"'\nshow 'date -u +%FT%TZ'\n")
 subprocess.run(['env','DEFER_RENDER=1','bash',str(base/'capture.sh'),name,str(script)],check=True)
while not Path('/work/www/kuberoot-0.1.1-router.field.3-amd64.tar.sig').exists():time.sleep(3)
subprocess.run(['bash',str(base/'build-fault-bundle.sh'),'crash','/work/field-fixes/root','0.1.1-router.field.3-crash'],check=True)
subprocess.run(['bash',str(base/'build-fault-bundle.sh'),'console','/work/field-fixes/root','0.1.1-router.field.3-console'],check=True)
deadline=time.monotonic()+1200
while not healthy():
 if time.monotonic()>deadline:raise SystemExit('old trial has not automatically recovered')
 time.sleep(3)
capture('t-R2-kuberoot-field3-install','python3 /work/field/R2-upgrade-trial.py 0.1.1-router.field.3 --timeout 300')
r=json.loads((base/'raw/trial-field.3.summary.json').read_text())
if r.get('timeout') or r.get('unsafe_promotion'):raise SystemExit('normal field3 not healthy')
capture('t-R2-kuberoot-field3-crash','python3 /work/field/R2-upgrade-trial.py 0.1.1-router.field.3-crash --expect-reject --timeout 600')
r=json.loads((base/'raw/trial-field.3-crash.summary.json').read_text())
if r.get('timeout') or r.get('unsafe_promotion'):raise SystemExit('fixed crash did not recover')
subprocess.run(['python3',str(base/'R2-power-cut-chain.py')],check=True)
