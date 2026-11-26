#!/usr/bin/env python3
import argparse,datetime,json,os,subprocess,time
from pathlib import Path
p=argparse.ArgumentParser();p.add_argument('--reboot',action='store_true');a=p.parse_args()
os.environ['KUBECONFIG']='/work/cluster-r1.conf'
base=['kubectl','--request-timeout=2s'];out=Path('/work/field/raw/R1-address'+('-reboot' if a.reboot else '')+'.jsonl')
def run(args,obj=None,quiet=False):
 if not quiet:print('$ '+' '.join(base+args),flush=True)
 p=subprocess.run(base+args,input=json.dumps(obj) if obj else None,text=True,capture_output=True,timeout=8)
 r={'at':time.time(),'command':base+args,'exit':p.returncode,'stdout':p.stdout,'stderr':p.stderr}
 with out.open('a') as f:f.write(json.dumps(r)+'\n')
 if not quiet:print(p.stdout+p.stderr,flush=True)
 return p
interface={'apiVersion':'router.kuberoot.dev/v1alpha1','kind':'Interface','metadata':{'name':'field-wan'},'spec':{'link':'eth0','addresses':['10.0.2.15/24']}}
run(['apply','-f','-'],interface)
time.sleep(2)
guard=json.loads(run(['get','safeguard','default','-o','json'],quiet=True).stdout)
rev=guard['status'].get('pending') or guard['status']['running']
run(['patch','safeguard','default','--type=merge','-p',json.dumps({'spec':{'confirm':rev,'confirmWithin':'60s'}})])
time.sleep(2)
print(datetime.datetime.now(datetime.timezone.utc).isoformat(),flush=True)
start=time.monotonic();interface['spec']['addresses']=['10.0.2.16/24']
run(['apply','-f','-'],interface)
lost=False;reset=False;recovered=None
while time.monotonic()-start<150:
 elapsed=time.monotonic()-start
 if a.reboot and not reset and elapsed>=20:
  print('$ python3 /work/field/qmp.py r1 system_reset',flush=True)
  subprocess.run(['python3','/work/field/qmp.py','r1','system_reset'],check=True);reset=True
 r=run(['get','safeguard','default','-o','json'],quiet=True)
 if r.returncode:lost=True
 else:
  status=json.loads(r.stdout)['status']
  if lost and status.get('rolledBack') and not status.get('pending'):
   recovered=time.monotonic()-start;print(json.dumps({'elapsed_s':recovered,'status':status}),flush=True);break
 if int(elapsed)%15<3:print(json.dumps({'at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'elapsed_s':elapsed,'management_available':r.returncode==0}),flush=True)
 time.sleep(2)
summary={'test_id':'R1','phase':'management-address','reboot_at_20s':a.reboot,'management_lost':lost,'recovered_s':recovered,'method':'production eth0 hostfwd; separate phase1 multicast client->peer forwarding observer','pass_management':recovered is not None and recovered<=(120 if a.reboot else 90)}
out.with_suffix('.summary.json').write_text(json.dumps(summary,indent=2)+'\n');print(json.dumps(summary),flush=True)
if recovered is None:raise SystemExit('No automatic recovery: preserve state; no observer repair performed')
interface['spec']['addresses']=['10.0.2.15/24'];run(['apply','-f','-'],interface)
