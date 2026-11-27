#!/usr/bin/env python3
import argparse,datetime,json,os,subprocess,time
from pathlib import Path
p=argparse.ArgumentParser();p.add_argument('version');p.add_argument('--expect-reject',action='store_true');p.add_argument('--timeout',type=int,default=1100);a=p.parse_args()
os.environ['KUBECONFIG']='/work/trust/admin.kubeconfig';base=['kubectl','--server','https://127.0.0.1:5010','--request-timeout=3s']
name=a.version.replace('0.1.1-router.','trial-');out=Path('/work/field/raw')/(name+'.jsonl');console=Path('/work/vms/r1/console.log');offset=console.stat().st_size
sha=Path('/work/www/kuberoot-'+a.version+'-amd64.tar.sha256').read_text().split()[0]
obj={'apiVersion':'node.kuberoot.dev/v1alpha1','kind':'Upgrade','metadata':{'name':name},'spec':{'url':'http://10.0.2.2:8088/kuberoot-'+a.version+'-amd64.tar','sha256':sha,'reboot':True}}
print(datetime.datetime.now(datetime.timezone.utc).isoformat(),flush=True)
print(json.dumps(obj,indent=2),flush=True);print('$ '+' '.join(base+['apply','-f','-']),flush=True)
subprocess.run(base+['apply','-f','-'],input=json.dumps(obj),text=True,check=True)
start=time.monotonic();seen=False;last=None;summary=None
while time.monotonic()-start<a.timeout:
 q=subprocess.run(base+['get','bootentries','-o','json'],capture_output=True,text=True)
 row={'at':time.time(),'elapsed_s':time.monotonic()-start,'exit':q.returncode,'stdout':q.stdout,'stderr':q.stderr}
 with out.open('a') as f:f.write(json.dumps(row)+'\n')
 if q.returncode==0:
  entries=json.loads(q.stdout)['items'];booted=next((e['status'] for e in entries if e['status'].get('booted')),None)
  if booted:
   state=(booted.get('release'),booted.get('state'),booted.get('triesLeft'))
   if state!=last:print(datetime.datetime.now(datetime.timezone.utc).isoformat(),json.dumps(booted),flush=True);last=state
   if booted.get('release')==a.version:seen=True
   done=seen and booted.get('state')=='Good' and ((booted.get('release')!=a.version) if a.expect_reject else True)
   unsafe=seen and a.expect_reject and booted.get('release')==a.version and booted.get('state')=='Good'
   if done or unsafe:
    observer=json.loads(Path('/work/field/raw/R2-forwarding-observer.jsonl').read_text().splitlines()[-1])
    summary={'version':a.version,'elapsed_s':time.monotonic()-start,'booted':booted,'expect_reject':a.expect_reject,'unsafe_promotion':unsafe or (booted.get('release')==a.version and not observer.get('ok',False)),'forwarding_observation':observer,'entries':entries};break
 time.sleep(3)
if summary is None:summary={'version':a.version,'timeout':True,'elapsed_s':time.monotonic()-start,'target_boot_seen':seen}
out.with_suffix('.summary.json').write_text(json.dumps(summary,indent=2)+'\n')
with console.open('rb') as f:f.seek(offset);out.with_suffix('.serial.txt').write_bytes(f.read())
print(datetime.datetime.now(datetime.timezone.utc).isoformat(),json.dumps(summary),flush=True)
# A measured failure is valid capture evidence; timeout is explicit in summary.
