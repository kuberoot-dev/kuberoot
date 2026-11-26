#!/usr/bin/env python3
"""Real Kubernetes optimistic concurrency and rejected-revision replay."""
import json,os,subprocess,time
from pathlib import Path
os.environ['KUBECONFIG']='/work/cluster-r1.conf'
base=['kubectl','--request-timeout=5s']
out=Path('/work/field/raw/R3-kuberoot-conflicts.jsonl')
def run(args,obj=None):
 p=subprocess.run(base+args,input=json.dumps(obj) if obj else None,text=True,capture_output=True)
 r={'at':time.time(),'command':base+args,'exit':p.returncode,'stdout':p.stdout,'stderr':p.stderr}
 with out.open('a') as f:f.write(json.dumps(r)+'\n')
 print(json.dumps(r),flush=True);return p
route={'apiVersion':'router.kuberoot.dev/v1alpha1','kind':'Route','metadata':{'name':'field-conflict'},'spec':{'destination':'203.0.113.0/24','gateway':'10.255.0.2','link':'eth2'}}
run(['apply','-f','-'],route)
r=run(['get','route','field-conflict','-o','json']);old=json.loads(r.stdout)
a=json.loads(r.stdout);a['spec']['metric']=101
run(['replace','-f','-'],a)
old['spec']['metric']=102
stale=run(['replace','-f','-'],old)
if stale.returncode==0:raise SystemExit('stale writer unexpectedly accepted')
run(['get','route','field-conflict','-o','yaml'])
run(['delete','route','field-conflict'])
