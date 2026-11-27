#!/usr/bin/env python3
import json,os,subprocess,time,re
from pathlib import Path
os.environ['KUBECONFIG']='/work/cluster-r1.conf'
raw=Path('/work/field/raw'); rows=[]
def run(cmd,inp=None):
 print(time.strftime('%FT%TZ',time.gmtime()),'$',' '.join(cmd),flush=True)
 p=subprocess.run(cmd,input=inp,text=True,capture_output=True,timeout=40);print(p.stdout+p.stderr,flush=True);rows.append({'at':time.time(),'command':cmd,'exit':p.returncode,'stdout':p.stdout,'stderr':p.stderr});return p
def kube(args,obj=None):return run(['kubectl','--request-timeout=5s']+args,json.dumps(obj) if obj else None)
def shell(s):return run(['python3','/work/field/console.py','r1',s,'--timeout','30'])
obj={'apiVersion':'router.kuberoot.dev/v1alpha1','kind':'FirewallRule','metadata':{'name':'field-drift-sentinel'},'spec':{'from':'wan','to':'self','protocol':'udp','ports':[59999],'action':'Drop'}}
kube(['apply','-f','-'],obj);time.sleep(3)
g=json.loads(kube(['get','safeguard','default','-o','json']).stdout)['status'];rev=g.get('pending') or g['running'];kube(['patch','safeguard','default','--type=merge','-p',json.dumps({'spec':{'confirm':rev}})])
p=shell('nft -a list chain inet kuberoot_router input');matches=re.findall(r'comment "rule field-drift-sentinel" # handle (\d+)',p.stdout)
if not matches:raise SystemExit('sentinel rule missing before injection')
start=time.monotonic();shell('nft delete rule inet kuberoot_router input handle '+matches[-1]);found=None
while time.monotonic()-start<35:
 p=shell('nft -a list chain inet kuberoot_router input')
 if re.search(r'comment "rule field-drift-sentinel" # handle \d+',p.stdout):found=time.monotonic()-start;break
 time.sleep(1)
summary={'test_id':'R3','repair_observed_s':found,'pass':found is not None and found<=30,'instrumentation':'explicit field.4-console serial shell; only one rule removed; table retained'}
(raw/'R3-kuberoot-drift.json').write_text(json.dumps(rows,indent=2)+'\n');(raw/'R3-kuberoot-drift.summary.json').write_text(json.dumps(summary,indent=2)+'\n');print(json.dumps(summary),flush=True)
kube(['delete','firewallrule','field-drift-sentinel'])
