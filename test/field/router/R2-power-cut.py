#!/usr/bin/env python3
"""Seeded hard stops at observed slot-write/selection phases, with real recovery."""
import argparse,json,os,random,signal,subprocess,time
from pathlib import Path
p=argparse.ArgumentParser();p.add_argument('trial',type=int);p.add_argument('phase',choices=['write','selection']);a=p.parse_args()
base=Path('/work/field/raw');out=base/f'R2-powercut-{a.trial:02}.jsonl'
if out.exists():raise SystemExit('existing trial preserved')
nodeenv=dict(os.environ,KUBECONFIG='/work/trust/admin.kubeconfig');clusterenv=dict(os.environ,KUBECONFIG='/work/cluster-r1.conf')
def run(args,obj=None,node=True,quiet=False):
 cmd=['kubectl','--request-timeout=2s']+(['--server','https://127.0.0.1:5010'] if node else [])+args
 if not quiet:print('$ '+' '.join(cmd),flush=True)
 p=subprocess.run(cmd,env=nodeenv if node else clusterenv,input=json.dumps(obj) if obj else None,text=True,capture_output=True,timeout=8)
 with out.open('a') as f:f.write(json.dumps({'at':time.time(),'command':cmd,'exit':p.returncode,'stdout':p.stdout,'stderr':p.stderr})+'\n')
 if not quiet:print(p.stdout+p.stderr,flush=True)
 return p
route={'apiVersion':'router.kuberoot.dev/v1alpha1','kind':'Route','metadata':{'name':'field-acked-state'},'spec':{'destination':'198.51.100.0/24','gateway':'10.255.0.2','link':'eth2','metric':a.trial}}
run(['apply','-f','-'],route,node=False);time.sleep(2)
g=json.loads(run(['get','safeguard','default','-o','json'],node=False,quiet=True).stdout)['status'];rev=g.get('pending') or g['running']
run(['patch','safeguard','default','--type=merge','-p',json.dumps({'spec':{'confirm':rev}})],node=False);time.sleep(2)
version='0.1.1-router.field.3';sha=Path('/work/www/kuberoot-'+version+'-amd64.tar.sha256').read_text().split()[0];name=f'field-powercut-{a.trial:02}'
obj={'apiVersion':'node.kuberoot.dev/v1alpha1','kind':'Upgrade','metadata':{'name':name},'spec':{'url':'http://10.0.2.2:8088/kuberoot-'+version+'-amd64.tar','sha256':sha,'reboot':False}}
if run(['apply','-f','-'],obj).returncode:raise SystemExit('cannot begin upgrade')
threshold=random.Random(42100+a.trial).randint(52,90);deadline=time.monotonic()+180;seen=None
while time.monotonic()<deadline:
 q=run(['get','upgrade',name,'-o','json'],quiet=True)
 if q.returncode:time.sleep(.1);continue
 st=json.loads(q.stdout)['status']
 if st.get('phase')=='Failed':raise SystemExit('upgrade failed before selected fault')
 if (a.phase=='write' and st.get('phase')=='Writing' and threshold<=st.get('progress',0)<95) or (a.phase=='selection' and st.get('phase')=='Staged'):
  seen=st;break
 time.sleep(.05)
if seen is None:raise SystemExit('selected phase was not observed; no cut injected')
console=Path('/work/vms/r1/console.log');offset=console.stat().st_size
pid=int(Path('/work/vms/r1/pid').read_text());cut=time.monotonic();wall=time.time();print(time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime()),'power cut',pid,json.dumps(seen),flush=True)
os.kill(pid,signal.SIGKILL)
for _ in range(100):
 try:
  if Path(f'/proc/{pid}/stat').read_text().split(')')[1].split()[0]=='Z':break
 except FileNotFoundError:break
 time.sleep(.05)
Path('/work/vms/r1/pid').unlink(missing_ok=True)
for attempt in range(20):
 restart=subprocess.run(['bash','/work/field/phase1-vm.sh','router','1'],capture_output=True,text=True)
 print(restart.stdout+restart.stderr,flush=True)
 if restart.returncode==0:break
 if 'write' not in restart.stderr or 'lock' not in restart.stderr:raise SystemExit('unexpected QEMU start failure')
 time.sleep(1)
else:raise SystemExit('QEMU image lock did not clear after power cut')
api=None;forward=None;retained=False;boot=None;deadline=time.monotonic()+240
while time.monotonic()<deadline:
 q=run(['get','bootentries','-o','json'],quiet=True)
 if q.returncode==0:
  if api is None:api=time.monotonic()-cut
  entries=json.loads(q.stdout)['items'];boot=next((e['status'] for e in entries if e['status'].get('booted')),None)
  state=run(['get','route','field-acked-state','-o','json'],node=False,quiet=True)
  retained=state.returncode==0 and json.loads(state.stdout)['spec'].get('metric')==a.trial
  observer=json.loads(Path('/work/field/raw/R2-forwarding-observer.jsonl').read_text().splitlines()[-1])
  if observer.get('ok') and observer['at']>=wall and forward is None:forward=time.monotonic()-cut
  if retained and forward is not None and boot and boot.get('state')=='Good':break
 time.sleep(2)
summary={'trial':a.trial,'phase':a.phase,'fault_seed':42100+a.trial,'threshold':threshold,'observed_at_cut':seen,'cut_wall_time':wall,'first_api_s':api,'first_forward_s':forward,'acked_state_retained':retained,'booted':boot,'recoverable':retained and forward is not None and boot is not None and boot.get('state')=='Good','recovery_s':time.monotonic()-cut,'manual_repair':False}
out.with_suffix('.summary.json').write_text(json.dumps(summary,indent=2)+'\n')
with console.open('rb') as f:f.seek(offset);out.with_suffix('.serial.txt').write_bytes(f.read())
print(json.dumps(summary),flush=True)
if not summary['recoverable']:raise SystemExit('failed recovery: preserve evidence and stop destructive trials')
