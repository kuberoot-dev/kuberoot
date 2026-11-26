#!/usr/bin/env python3
"""Thirty seeded VM power cuts with external fsync acknowledgements.
Upgrade phases require a product-specific hook that returns only at that phase.
No hook means ordinary writes only; this does NOT claim upgrade-phase coverage.
"""
import argparse,datetime,json,os,pathlib,random,shlex,signal,subprocess,sys,time
from qmp import command
p=argparse.ArgumentParser();p.add_argument('--golden',type=pathlib.Path,required=True);p.add_argument('--output',type=pathlib.Path,required=True);p.add_argument('--product',required=True);p.add_argument('--image-digest',required=True);p.add_argument('--source-commit',required=True);p.add_argument('--seed',type=int,default=20261125);p.add_argument('--phase-hook',type=pathlib.Path);p.add_argument('--repetition',type=int,choices=range(1,31));a=p.parse_args()
scripts=pathlib.Path(__file__).resolve().parent;root=pathlib.Path('/work/field-edge')
assert a.output.is_relative_to(root) and a.golden.is_relative_to(root)
a.output.mkdir(parents=True,exist_ok=True)
def run(args,**kw):return subprocess.run([str(x) for x in args],check=True,**kw)
def gateway(cmd,check=True):return subprocess.run([str(scripts/'gateway-command.sh'),cmd],capture_output=True,text=True,timeout=20,check=check)
def kube(*args):return subprocess.check_output(['kubectl','--request-timeout=5s',*args],text=True,timeout=8)
base=a.golden/'system.qcow2'
if not base.exists():
 run(['qemu-img','convert','-c','-f','raw','-O','qcow2',a.golden/'system.raw',base])
 run(['qemu-img','compare','-f','raw','-F','qcow2',a.golden/'system.raw',base])
phases=os.environ.get('POWER_PHASES','writes,download,inactive-write,boot-selection,probation').split(',') if a.phase_hook else ['writes']
for repetition in ([a.repetition-1] if a.repetition else range(30)):
 rng=random.Random(a.seed+repetition)
 phase=phases[repetition%len(phases)];name=f'E3-{a.product}-{repetition+1:02d}';d=a.output/name;d.mkdir()
 run(['qemu-img','convert','-f','qcow2','-O','raw',base,d/'system.raw']);run(['cp',a.golden/'vars.fd',d/'vars.fd'])
 env=dict(os.environ,VM_DIR=str(d));
 if a.phase_hook:env['BLOCK_FAULTS']='1'
 errors=[];metrics={};cut_epoch=None;recorder=None;vga=None;offset=None
 try:
  run(['bash','-c','source "$1/lib.sh"; vm_start 1','_',scripts],env=env)
  deadline=time.monotonic()+300
  while time.monotonic()<deadline:
   r=gateway('python3 -c \'import urllib.request; assert urllib.request.urlopen("http://10.78.0.11:30080/http",timeout=2).read()==b"W"*1024; assert urllib.request.urlopen("http://10.78.0.11:30081/http",timeout=2).read()==b"W"*1024\'',check=False)
   if r.returncode==0:break
   time.sleep(2)
  else:raise RuntimeError('W did not become healthy before injection')
  subprocess.run(['bash',str(scripts/'prepare-observer.sh')],env=env,check=True)
  before_node=json.loads(kube('get','nodes','-o','json'))['items'][0];before=before_node['status']['nodeInfo']['bootID'];metrics['kubelet_version_before']=before_node['status']['nodeInfo']['kubeletVersion']
  gateway(f'nohup python3 /home/observer/observer.py --http http://10.78.0.11:30080 --append http://10.78.0.11:30081 --kubeconfig /home/observer/kubeconfig --seconds 2400 --stop-file /home/observer/{name}.stop --output /home/observer/{name}.jsonl >/home/observer/{name}.log 2>&1 </dev/null &')
  time.sleep(5)
  offset=rng.uniform(2,8) if phase=='writes' else rng.uniform(0,.25)
  if phase!='writes':run([a.phase_hook,phase],env=dict(env,FIELD_PHASE_DELAY=str(offset)))
  else:time.sleep(offset)
  recorder=subprocess.Popen(['asciinema','rec','--cols','110','--rows','30','-q','-c',shlex.join(['python3',str(scripts/'serial-follow.py'),str(d/'serial.log'),'--from-end','--stop-file',str(d/'serial.stop')]),str(d/'recovery.cast')],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,start_new_session=True)
  if a.product.startswith('talos'):
   vga=subprocess.Popen(['python3',str(scripts/'vga-capture.py'),'--qmp',str(d/'qmp.sock'),'--output',str(d/'vga'),'--stop-file',str(d/'serial.stop')],stdout=(d/'vga-capture.log').open('w'),stderr=subprocess.STDOUT)
  time.sleep(.2)
  kill_command=['kill','-9',str(int((d/'pid').read_text()))]
  print('$ ',end='',flush=True)
  for ch in shlex.join(kill_command): print(ch,end='',flush=True);time.sleep(.01)
  print(flush=True);power_cut_epoch=time.time();cut_epoch=power_cut_epoch
  if (d/'phase-evidence.json').exists():
   evidence=json.loads((d/'phase-evidence.json').read_text());cut_epoch=evidence['pause_epoch'];metrics['phase_evidence']=evidence;metrics['power_cut_epoch']=power_cut_epoch
  run(kill_command)
  print(json.dumps({'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'phase':phase,'seeded_delay_seconds':offset}),flush=True)
  time.sleep(2)
  (d/'before-cut-serial.log').write_bytes((d/'serial.log').read_bytes())
  run(['bash','-c','source "$1/lib.sh"; vm_start 1','_',scripts],env=env)
  deadline=time.monotonic()+250
  while time.monotonic()<deadline:
   rows=gateway(f'cat /home/observer/{name}.jsonl').stdout
   # A concurrent read can end in the middle of one final append.
   if not rows.endswith('\n'):rows=rows.rsplit('\n',1)[0]+'\n'
   (d/'observer.jsonl').write_text(rows)
   r=subprocess.run(['python3',str(scripts/'analyse-observer.py'),str(d/'observer.jsonl'),'--fault-epoch',str(cut_epoch)],capture_output=True,text=True)
   if r.returncode==0:
    metrics.update(json.loads(r.stdout))
    if metrics['recovery_seconds_first_60s_sustained_success'] is not None:break
   time.sleep(5)
  gateway(f'touch /home/observer/{name}.stop')
  for i in range(15):
   if gateway(f'test -f /home/observer/{name}.jsonl.done',check=False).returncode==0:break
   time.sleep(1)
  (d/'observer.jsonl').write_text(gateway(f'cat /home/observer/{name}.jsonl').stdout)
  with (d/'records').open('wb') as f:
   run([scripts/'gateway-command.sh','curl --fail --max-time 10 http://10.78.0.11:30081/records'],stdout=f,timeout=15)
  r=subprocess.run(['python3',str(scripts/'check-records.py'),str(d/'observer.jsonl'),str(d/'records')],capture_output=True,text=True)
  metrics.update(json.loads(r.stdout))
  if r.returncode:errors.append('acknowledged record loss or corruption')
  after=json.loads(kube('get','nodes','-o','json'))['items'][0]['status']['nodeInfo']['bootID'];metrics['fresh_boot_id']=after!=before
  if after==before:errors.append('fresh bootID not observed')
  if a.product.startswith('kuberoot'):
   node_env=dict(os.environ,KUBECONFIG=os.environ['NODE_KUBECONFIG'])
   slots=json.loads(subprocess.check_output(['kubectl','--request-timeout=5s','get','bootentries','-o','json'],text=True,env=node_env,timeout=8))
   (d/'bootentries-after.json').write_text(json.dumps(slots,indent=2))
   booted=[s for s in slots['items'] if s.get('status',{}).get('booted')]
   metrics['valid_boot_slot']=len(booted)==1 and booted[0]['status']['state']=='Good'
   if not metrics['valid_boot_slot']:errors.append('no single booted Good slot after sustained W recovery')
  else:
   node=json.loads(kube('get','nodes','-o','json'))['items'][0]
   (d/'node-after.json').write_text(json.dumps(node,indent=2))
   metrics['booted_os_image']=node['status']['nodeInfo']['osImage'];metrics['kubelet_version_after']=node['status']['nodeInfo']['kubeletVersion']
   metrics['slot_observation']='fresh bootID and running OS version; no inferred A/B slot status'
  recovery=metrics.get('recovery_seconds_first_60s_sustained_success')
  if recovery is None or recovery>180:errors.append('no sustained W recovery within 180 seconds')
  if metrics.get('driver_overloads'):errors.append('observer dispatch overload; run invalid')
 except Exception as exc:errors.append(str(exc))
 finally:
  try:gateway(f'touch /home/observer/{name}.stop',check=False)
  except Exception:pass
  try:command(str(d/'qmp.sock'),'quit')
  except Exception:pass
  time.sleep(2)
  if recorder:
   (d/'serial.stop').touch();recorder.wait(timeout=10)
   if vga:vga.wait(timeout=60)
   timing=json.loads(subprocess.check_output(['python3','/work/scripts/render.py',str(d/'recovery.cast')],text=True))
   subprocess.run(['python3',str(scripts/'bound-cast.py'),str(d/'recovery.retimed.cast')],check=True,stdout=(d/'recovery-speed.json').open('w'))
   hold=str(max(5,15.1-timing['playback_seconds']))
   run(['/work/bin/agg','-q','--font-size','16','--theme','monokai','--last-frame-duration',hold,d/'recovery.retimed.cast',d/'recovery.gif'])
 result={'scenario_id':'E3','product':a.product,'image_digest':a.image_digest,'source_commit':a.source_commit,'repetition':repetition+1,'fault_seed':a.seed+repetition,'seed_base':a.seed,'fault_offset_seconds':offset,'fault_phase':phase,'fault_epoch':cut_epoch,'vcpu':2,'ram_mib':4096,'topology':'fresh raw clone + fresh OVMF vars; same multicast LAN/gateway; observer outside DUT','metrics':metrics,'acknowledged_record_loss':metrics.get('acknowledged_record_loss'),'recovery_seconds':metrics.get('recovery_seconds_first_60s_sustained_success'),'outage_seconds':None,'manual_actions':[],'status':'invalid-fixture' if cut_epoch is None or metrics.get('driver_overloads') else 'failed' if errors else 'passed-W','reason':'; '.join(errors) if errors else 'external W, acknowledged-record and fresh bootID gates passed; native slot/version observations retained','artifact_paths':[str(d)],'time_utc':datetime.datetime.now(datetime.timezone.utc).isoformat()}
 (d/'result.json').write_text(json.dumps(result,indent=2));print(json.dumps(result),flush=True)
 with (a.output/'runs.jsonl').open('a') as f:f.write(json.dumps(result)+'\n')
 if errors:
  # Preserve exact failed disk bytes as a verified delta against the kept golden.
  run(['python3',scripts/'share-disk-blocks.py',d/'system.raw',base,d/'failed.qcow2','--source-format','raw'])
  run(['qemu-img','compare','-f','raw','-F','qcow2',d/'system.raw',d/'failed.qcow2'])
 (d/'system.raw').unlink()
