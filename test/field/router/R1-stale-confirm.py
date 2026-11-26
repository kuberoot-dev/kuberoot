#!/usr/bin/env python3
import json,os,subprocess,time
from pathlib import Path
os.environ['KUBECONFIG']='/work/cluster-r1.conf'
out=Path('/work/field/raw/R1-stale-confirm.jsonl')
def cmd(args,obj=None):
 print('$ kubectl '+' '.join(args),flush=True)
 p=subprocess.run(['kubectl','--request-timeout=5s']+args,input=json.dumps(obj) if obj else None,capture_output=True,text=True)
 with out.open('a') as f:f.write(json.dumps({'at':time.time(),'command':args,'exit':p.returncode,'stdout':p.stdout,'stderr':p.stderr})+'\n')
 print(p.stdout+p.stderr,flush=True)
 if p.returncode:raise RuntimeError('kubectl failed')
 return p.stdout
def guard():return json.loads(cmd(['get','safeguard','default','-o','json']))['status']
r={'apiVersion':'router.kuberoot.dev/v1alpha1','kind':'Route','metadata':{'name':'field-stale-confirm'},'spec':{'destination':'203.0.113.0/24','gateway':'10.255.0.2','link':'eth2','metric':100}}
cmd(['apply','-f','-'],r);time.sleep(2);n=guard()['pending']
r['spec']['metric']=101;cmd(['apply','-f','-'],r);time.sleep(2);n1=guard()['pending']
cmd(['patch','safeguard','default','--type=merge','-p',json.dumps({'spec':{'confirm':n}})]);time.sleep(2);after=guard()
summary={'N':n,'N_plus_1':n1,'after_stale_confirm':after,'stale_did_not_confirm_next':n!=n1 and after.get('pending')==n1 and after.get('confirmed')!=n1}
out.with_suffix('.summary.json').write_text(json.dumps(summary,indent=2)+'\n');print(json.dumps(summary),flush=True)
cmd(['delete','route','field-stale-confirm'])
if not summary['stale_did_not_confirm_next']:raise SystemExit('stale confirmation safety failed')
