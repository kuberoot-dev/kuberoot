#!/usr/bin/env python3
"""One storage-fault repetition on a fresh disk, measured from outside the DUT.
A storage/W subcase alone does not establish upgrade-safety coverage.
"""
import argparse,datetime,json,os,pathlib,shlex,subprocess,time,threading,shutil
from qmp import command
p=argparse.ArgumentParser();p.add_argument('--golden',type=pathlib.Path,required=True);p.add_argument('--output',type=pathlib.Path,required=True);p.add_argument('--case',choices=['100-iops','20-iops','full-90','full-95','full-100','eio'],required=True);p.add_argument('--product',required=True);p.add_argument('--upgrade-hook',type=pathlib.Path);a=p.parse_args()
root=pathlib.Path('/work/field-edge');scripts=pathlib.Path(__file__).resolve().parent
assert a.output.is_relative_to(root) and a.golden.is_relative_to(root)
d=a.output;d.mkdir(parents=True,exist_ok=False);name=f'E4-{a.product}-{a.case}'
for f in ['system.raw','vars.fd']:subprocess.run(['cp','--sparse=always',str(a.golden/f),str(d/f)],check=True)
env=dict(os.environ,VM_DIR=str(d),BLOCK_FAULTS='1');errors=[];metrics={};relieved=None;fault=None;recorder=None;fault_active=False
G=str(scripts/'gateway-command.sh')
def gateway(cmd,check=True):return subprocess.run([G,cmd],check=check,text=True,capture_output=True,timeout=45)
def show(args):
 print('$ ',end='',flush=True)
 for ch in shlex.join([str(x) for x in args]):print(ch,end='',flush=True);time.sleep(.012)
 print(flush=True);return subprocess.run([str(x) for x in args],check=True,env=env)
def block(mode):show(['python3',scripts/'block-fault.py',d/'qmp.sock',mode])
def occupancy(path):
 r=gateway('curl --silent --show-error --max-time 40 -X POST http://10.78.0.11:30082/'+path,check=False)
 print('$ '+shlex.join([G,'curl -X POST http://10.78.0.11:30082/'+path]),flush=True);print(r.stdout,flush=True)
 if r.returncode:raise RuntimeError(r.stderr)
 return json.loads(r.stdout)
def pull_observations():
 rows=gateway(f'cat /home/observer/{name}.jsonl').stdout
 if rows and not rows.endswith('\n'):rows=rows.rsplit('\n',1)[0]+'\n'
 (d/'observer.jsonl').write_text(rows)
def recover_metrics():
 r=subprocess.check_output(['python3',str(scripts/'analyse-observer.py'),str(d/'observer.jsonl'),'--fault-epoch',str(relieved)],text=True)
 return json.loads(r)
try:
 subprocess.run(['bash','-c','source "$1/lib.sh"; vm_start 1','_',str(scripts)],check=True,env=env)
 for i in range(150):
  r=gateway('curl --fail --max-time 2 http://10.78.0.11:30080/http >/dev/null && curl --fail --max-time 2 http://10.78.0.11:30081/http >/dev/null',check=False)
  if r.returncode==0:break
  time.sleep(2)
 else:raise RuntimeError('W unavailable before storage injection')
 subprocess.run(['bash',str(scripts/'prepare-observer.sh')],env=env,check=True)
 # The injector is preloaded on the golden disk, with a critical priority and
 # independent HTTP endpoint so relief does not depend on a responsive API.
 if a.case.startswith('full-'):metrics['initial_filesystem']=occupancy('relieve')
 gateway(f'nohup python3 /home/observer/observer.py --http http://10.78.0.11:30080 --append http://10.78.0.11:30081 --kubeconfig /home/observer/kubeconfig --seconds 1200 --stop-file /home/observer/{name}.stop --output /home/observer/{name}.jsonl >/home/observer/{name}.log 2>&1 </dev/null &')
 time.sleep(60)
 serial=shlex.join(['python3',str(scripts/'serial-follow.py'),str(d/'serial.log'),'--from-end','--stop-file',str(d/'serial.stop'),'--max-seconds','1000'])
 recorder=subprocess.Popen(['asciinema','rec','--cols','110','--rows','30','-q','-c',serial,str(d/'serial.cast')],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
 before=command(str(d/'qmp.sock'),'query-blockstats');before_time=time.monotonic()
 show(['date','-u']);fault=time.time();fault_active=True
 if a.case in ['100-iops','20-iops']:block(a.case)
 elif a.case=='eio':block('eio-on')
 else:
  # Guest fallocate reserves unwritten extents, normally only guest metadata
  # reaches the raw backend. Protect the shared PVC if a filesystem behaves
  # differently; a host-capacity abort invalidates the fixture, not the DUT.
  guard_stop=threading.Event();host_abort=[]
  def guard():
   while not guard_stop.wait(.25):
    free=shutil.disk_usage(d).free
    if free<768*1024**2:
     host_abort.append({'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'host_free_bytes':free})
     try:occupancy('relieve')
     except Exception as exc:host_abort.append({'relief_error':str(exc)})
     break
  monitor=threading.Thread(target=guard);monitor.start()
  try:metrics['full_filesystem']=occupancy('fill/'+a.case.split('-')[1])
  finally:guard_stop.set();monitor.join(timeout=45)
  metrics['host_capacity_guard']=host_abort
  if host_abort:raise RuntimeError('fixture capacity guard aborted fill before shared PVC exhaustion')
 (d/'fault-epoch').write_text(str(fault))
 if a.upgrade_hook:show([a.upgrade_hook,a.case])
 time.sleep(300 if 'iops' in a.case else 30 if a.case=='eio' else 120)
 show(['date','-u'])
 if a.case.startswith('full-'):metrics['relieved_filesystem']=occupancy('relieve')
 else:block('eio-off' if a.case=='eio' else 'unthrottle')
 fault_active=False
 relieved=time.time();(d/'relief-epoch').write_text(str(relieved))
 while time.time()-relieved<360:
  pull_observations();metrics.update(recover_metrics())
  if metrics.get('recovery_seconds_first_60s_sustained_success') is not None:break
  time.sleep(5)
 after=command(str(d/'qmp.sock'),'query-blockstats');duration=time.monotonic()-before_time
 writes=lambda r:sum(x['stats']['wr_bytes'] for x in r)
 metrics['virtual_write_bytes_per_day_during_trial']=(writes(after)-writes(before))*86400/duration
 (d/'block-counters.json').write_text(json.dumps({'before':before,'after':after,'observed_seconds':duration},indent=2))
 gateway(f'touch /home/observer/{name}.stop')
 for i in range(15):
  if gateway(f'test -f /home/observer/{name}.jsonl.done',check=False).returncode==0:break
  time.sleep(1)
 pull_observations();rows=[json.loads(x) for x in (d/'observer.jsonl').read_text().splitlines()]
 affected=[r for r in rows if r.get('utc') and fault<=datetime.datetime.fromisoformat(r['utc']).timestamp()<relieved]
 requests=[r for r in affected if r['kind'] in ['http','append']]
 metrics['fault_window_requests']=len(requests);metrics['fault_window_success_fraction']=sum(bool(r.get('ok')) for r in requests)/len(requests) if requests else None
 latencies=[o['latency_seconds'] for r in affected if r['kind']=='api' for o in r.get('api_operations',[])]
 metrics['fault_window_api_operation_count']=len(latencies);metrics['fault_window_api_p99_seconds']=sorted(latencies)[int(.99*(len(latencies)-1))] if latencies else None
 with (d/'records').open('wb') as f:
  subprocess.run([G,'curl --fail --max-time 20 http://10.78.0.11:30081/records'],stdout=f,check=True,timeout=30)
 r=subprocess.run(['python3',str(scripts/'check-records.py'),str(d/'observer.jsonl'),str(d/'records')],capture_output=True,text=True)
 metrics.update(json.loads(r.stdout))
 if r.returncode:errors.append('acknowledged record loss or corruption')
 recovery=metrics.get('recovery_seconds_first_60s_sustained_success')
 if recovery is None or recovery>300:errors.append('no sustained W recovery within 300 seconds after relief')
 if a.case=='100-iops':
  if metrics['fault_window_success_fraction'] is None or metrics['fault_window_success_fraction']<.999:errors.append('W success fraction below 99.9% at 100 IOPS')
  if metrics['fault_window_api_p99_seconds'] is None or metrics['fault_window_api_p99_seconds']>=1:errors.append('API p99 not below 1 second at 100 IOPS')
 if metrics.get('driver_overloads'):errors.append('observer dispatch overload')
except Exception as exc:errors.append(str(exc))
finally:
 try:
  if fault_active:
   if a.case in ['100-iops','20-iops']:block('unthrottle')
   elif a.case=='eio':block('eio-off')
   else:metrics['cleanup_filesystem']=occupancy('relieve')
 except Exception as exc:errors.append('fault cleanup: '+str(exc))
 try:gateway(f'touch /home/observer/{name}.stop',check=False)
 except Exception:pass
 try:command(str(d/'qmp.sock'),'quit')
 except Exception:pass
 if recorder:
  (d/'serial.stop').touch();recorder.wait(timeout=10)
  timing=json.loads(subprocess.check_output(['python3','/work/scripts/render.py',str(d/'serial.cast')],text=True))
  subprocess.run(['python3',str(scripts/'bound-cast.py'),str(d/'serial.retimed.cast')],check=True,stdout=(d/'serial-speed.json').open('w'))
  subprocess.run(['/work/bin/agg','-q','--font-size','16','--theme','monokai','--last-frame-duration',str(max(5,15.1-timing['playback_seconds'])),str(d/'serial.retimed.cast'),str(d/'serial.gif')],check=True)
record={'scenario_id':'E4','product':a.product,'source_commit':os.environ['SOURCE_COMMIT'],'image_digest':os.environ['IMAGE_DIGEST'],'vcpu':2,'ram_mib':4096,'topology':'fresh raw clone; QEMU throttle/blkdebug; guest-local fallocate; gateway observer','fault_seed':None,'fault_phase':a.case,'metrics':metrics,'acknowledged_record_loss':metrics.get('acknowledged_record_loss'),'recovery_seconds':metrics.get('recovery_seconds_first_60s_sustained_success'),'outage_seconds':None,'manual_actions':[],'status':'invalid-fixture-capacity' if metrics.get('host_capacity_guard') else 'failed' if errors else 'passed-W-subcase','reason':'; '.join(errors) if errors else 'storage/W subcase passed; upgrade safety is a separate mandatory lane','time_utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'upgrade_hook_executed':bool(a.upgrade_hook),'artifact_paths':[str(d)]}
(d/'result.json').write_text(json.dumps(record,indent=2));print(json.dumps(record),flush=True)
