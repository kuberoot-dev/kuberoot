#!/usr/bin/env python3
import json,os,subprocess,time,argparse
from pathlib import Path
p=argparse.ArgumentParser();p.add_argument('fault',choices=['return-route','vlan','firewall']);p.add_argument('mode',choices=['normal','reboot','controller']);a=p.parse_args()
raw=Path('/work/field/raw');name='R1-kuberoot-'+a.fault+'-'+a.mode;rows=[]
def run(args,obj=None,observer=False,quiet=False):
 cmd=['kubectl','--request-timeout=2s']+args
 env=dict(os.environ,KUBECONFIG='/work/cluster-r1.conf' if observer else '/work/field/private/r1-production.conf')
 if not quiet:print(time.strftime('%FT%TZ',time.gmtime()),'$',' '.join(cmd),flush=True)
 p=subprocess.run(cmd,env=env,input=json.dumps(obj) if obj else None,capture_output=True,text=True,timeout=8)
 rows.append({'at':time.time(),'observer':observer,'command':cmd,'exit':p.returncode,'stdout':p.stdout,'stderr':p.stderr})
 if not quiet:print(p.stdout+p.stderr,flush=True)
 return p
def obj(kind,name,spec):return {'apiVersion':'router.kuberoot.dev/v1alpha1','kind':kind,'metadata':{'name':name},'spec':spec}
route=obj('Route','field-production-return',{'destination':'172.31.250.0/24','gateway':'10.255.0.3','link':'eth2'})
transit=obj('Interface','transit',{'link':'eth2','addresses':['10.255.0.1/24']})
nonce=obj('Route','field-trial-generation',{'destination':'198.51.101.0/24','gateway':'10.255.0.2','link':'eth2','metric':int(time.time())%1000000})
run(['delete','interface','field-bad-vlan','--ignore-not-found'],observer=True)
run(['apply','-f','-'],nonce,True)
run(['apply','-f','-'],route,True);run(['apply','-f','-'],transit,True)
run(['patch','firewallzone','transit','--type=merge','-p','{"spec":{"input":"Accept"}}'],observer=True)
def confirm_baseline():
 for _ in range(40):
  p=run(['get','route','field-production-return','-o','json'],observer=True,quiet=True)
  if p.returncode==0:
   r=json.loads(p.stdout);st=r.get('status',{})
   if st.get('observedGeneration')==r['metadata']['generation'] and any(c['type']=='Applied' and c['status']=='True' for c in st.get('conditions',[])):break
  time.sleep(1)
 else:raise SystemExit('baseline route not applied')
 time.sleep(2)
 st=json.loads(run(['get','safeguard','default','-o','json'],observer=True).stdout)['status'];rev=st.get('pending') or st['running']
 run(['patch','safeguard','default','--type=merge','-p',json.dumps({'spec':{'confirmWithin':'60s','confirm':rev}})],observer=True)
 for _ in range(20):
  st=json.loads(run(['get','safeguard','default','-o','json'],observer=True,quiet=True).stdout)['status']
  if st.get('confirmed')==rev and st.get('running')==rev and not st.get('pending'):return rev
  time.sleep(1)
 raise SystemExit('baseline confirmation not observed')
confirm_baseline()
p=run(['get','safeguard','default','-o','json'])
if p.returncode:raise SystemExit('production preflight failed; no fault injected')
s=json.loads(p.stdout)['status'];run(['patch','safeguard','default','--type=merge','-p',json.dumps({'spec':{'confirmWithin':'60s','confirm':s.get('pending') or s['running']}})])
time.sleep(2);start=time.monotonic();wall=time.time();lost=False;recovered=None;injected=False
if a.fault=='return-route':run(['delete','route','field-production-return'])
elif a.fault=='firewall':run(['patch','firewallzone','transit','--type=merge','-p','{"spec":{"input":"Drop","management":false}}'])
else:
 vlan=obj('Interface','field-bad-vlan',{'link':'eth2.999','vlan':{'parent':'eth2','id':999},'addresses':['10.255.0.1/24']});transit['spec']['addresses']=['10.255.0.100/24']
 run(['apply','-f','-'],{'apiVersion':'v1','kind':'List','items':[transit,vlan]})
while time.monotonic()-start<160:
 elapsed=time.monotonic()-start
 if a.mode!='normal' and elapsed>=20 and not injected:
  cmd=['python3','/work/field/qmp.py','r1','system_reset'] if a.mode=='reboot' else ['python3','/work/field/console.py','r1','old=$(/bin/busybox pidof kuberoot-router); printf "before_pid=%s\\n" "$old"; kill -KILL "$old"; /bin/busybox sleep 2; /bin/busybox pidof kuberoot-router']
  print(time.strftime('%FT%TZ',time.gmtime()),'$',' '.join(cmd),flush=True);subprocess.run(cmd,timeout=40);injected=True
 p=run(['get','safeguard','default','-o','json'],quiet=True)
 if p.returncode:lost=True
 else:
  s=json.loads(p.stdout)['status']
  if lost and s.get('rolledBack') and not s.get('pending'):recovered=time.monotonic()-start;break
 print(json.dumps({'at':time.time(),'elapsed_s':time.monotonic()-start,'available':p.returncode==0}),flush=True);time.sleep(2)
summary={'fault':a.fault,'mode':a.mode,'lost':lost,'recovered_s':recovered,'fault_wall_time':wall,'automatic_recovery':recovered is not None,'production_path':'pod -> SSH gateway observer -> TCP proxy in namespace172.31.250.2 -> gateway10.255.0.3 -> r1 eth2; no observer repair during trial'}
(raw/(name+'.json')).write_text(json.dumps(rows,indent=2)+'\n');(raw/(name+'.summary.json')).write_text(json.dumps(summary,indent=2)+'\n');print(json.dumps(summary),flush=True)
if recovered is None:raise SystemExit('No automatic recovery; stop this chain for diagnosis')
# Restore desired baseline after observing the rollback, then confirm it.
run(['apply','-f','-'],route,True);transit['spec']['addresses']=['10.255.0.1/24'];run(['apply','-f','-'],transit,True);run(['delete','interface','field-bad-vlan','--ignore-not-found'],observer=True);run(['patch','firewallzone','transit','--type=merge','-p','{"spec":{"input":"Accept"}}'],observer=True);time.sleep(3)
confirm_baseline()
