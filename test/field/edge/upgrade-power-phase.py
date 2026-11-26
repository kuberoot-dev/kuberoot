#!/usr/bin/env python3
"""Pause a real kuberoot upgrade at a verified power-cut phase.
The caller immediately kills QEMU; pause timing remains explicit evidence.
"""
import datetime,json,os,pathlib,shlex,subprocess,sys,time
from qmp import command
phase=sys.argv[1];assert phase in ['download','inactive-write','boot-selection','probation']
d=pathlib.Path(os.environ['VM_DIR']);assert str(d).startswith('/work/field-edge/')
scripts=pathlib.Path(__file__).resolve().parent;delay=float(os.environ['FIELD_PHASE_DELAY']);env=dict(os.environ,KUBECONFIG=os.environ['NODE_KUBECONFIG'])
def call(args):
 print('$ '+shlex.join(args),flush=True)
 return subprocess.run(args,text=True,capture_output=True,env=env,timeout=10)
def kube(*args):
 r=call(['kubectl','--request-timeout=5s',*args])
 if r.returncode:raise RuntimeError(r.stderr)
 return json.loads(r.stdout)
request={'apiVersion':'node.kuberoot.dev/v1alpha1','kind':'Upgrade','metadata':{'name':'field-power-cut'},'spec':{'url':os.environ['GOOD_BUNDLE_URL'],'sha256':os.environ['GOOD_BUNDLE_SHA256'],'reboot':phase=='probation'}}
(d/'upgrade-request.json').write_text(json.dumps(request,indent=2))
print('$ export KUBECONFIG='+shlex.quote(env['KUBECONFIG']),flush=True)
if phase=='download':
 subprocess.run(['bash',str(scripts/'wan-impairment.sh'),'slow'],check=True)
elif phase=='inactive-write':
 command(str(d/'qmp.sock'),'qom-set',{'path':'/objects/field-throttle','property':'limits','value':{'bps-total':5*1024**2}})
try:
 kube('create','-f',str(d/'upgrade-request.json'),'-o','json')
 def observed():
  if phase=='probation':
   obj=kube('get','bootentries','-o','json');current=next(x['status'] for x in obj['items'] if x['status'].get('booted'))
   return current['state']=='Trying',obj
  obj=kube('get','upgrade','field-power-cut','-o','json')
  return obj.get('status',{}).get('phase')=={'download':'Downloading','inactive-write':'Writing','boot-selection':'Staged'}[phase],obj
 end=time.monotonic()+360
 while time.monotonic()<end:
  try:
   match,obj=observed()
   if match:
    first=time.time();time.sleep(delay);match,second=observed()
    if not match:raise RuntimeError('phase ended before seeded offset; no power cut injected')
    command(str(d/'qmp.sock'),'stop');paused=time.time()
    state=command(str(d/'qmp.sock'),'query-status');assert state['status']=='paused'
    evidence={'phase':phase,'seeded_offset_seconds':delay,'first_observation_epoch':first,'pause_epoch':paused,'actual_offset_seconds':paused-first,'before_pause':second,'qmp_status':state,'note':'QMP stop freezes the verified phase before the caller types kill -9. Effective outage starts at pause; kill epoch is recorded separately.'}
    (d/'phase-evidence.json').write_text(json.dumps(evidence,indent=2));print(json.dumps(evidence),flush=True);break
  except (subprocess.TimeoutExpired,RuntimeError) as exc:
   if 'phase ended' in str(exc):raise
  time.sleep(.1)
 else:raise RuntimeError('requested real upgrade phase not observed within 360 seconds')
finally:
 if phase=='download':subprocess.run(['bash',str(scripts/'wan-impairment.sh'),'clean'],check=True)
