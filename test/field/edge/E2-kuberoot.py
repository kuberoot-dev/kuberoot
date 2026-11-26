#!/usr/bin/env python3
"""One real signed-upgrade failure experiment, with raw observations preserved.
Use a separate test-key image for injected signed faults; never bypass trust.
"""
import argparse,datetime,json,os,pathlib,shlex,shutil,subprocess,time
from qmp import command
from upgrade_observations import evaluate as evaluate_service, wait_for_service
p=argparse.ArgumentParser();p.add_argument('--golden',type=pathlib.Path,required=True);p.add_argument('--output',type=pathlib.Path,required=True);p.add_argument('--case',choices=['init-crash','pod-network','wrong-content','incompatible-state','tampered','untrusted','healthy-control'],required=True);p.add_argument('--bundle-url',required=True);p.add_argument('--bundle-sha256',required=True);p.add_argument('--target-version',required=True);p.add_argument('--product',required=True);p.add_argument('--repetition',type=int,default=1);p.add_argument('--deadline',type=float,default=1500);p.add_argument('--health-checks',type=pathlib.Path);a=p.parse_args()
root=pathlib.Path('/work/field-edge');scripts=pathlib.Path(__file__).resolve().parent;d=a.output;assert d.is_relative_to(root)
required=(a.golden/'system.raw').stat().st_blocks*512+1024**3
if shutil.disk_usage(root).free<required:raise SystemExit('Insufficient PVC headroom for a fresh raw clone plus evidence; prepare capacity before this trial')
d.mkdir(parents=True,exist_ok=False)
for f in ['system.raw','vars.fd']:subprocess.run(['cp','--sparse=always',str(a.golden/f),str(d/f)],check=True)
env=dict(os.environ,VM_DIR=str(d),FAULT_FIXTURE="1");node_env=dict(env,KUBECONFIG=os.environ['NODE_KUBECONFIG']);G=str(scripts/'gateway-command.sh');name=d.name;errors=[];metrics={};observer_started=False;recorder=None;fault_verified=False
negative=a.case in ['tampered','untrusted']
control=a.case=='healthy-control'
health_checks=json.loads(a.health_checks.read_text()) if a.health_checks else None
if health_checks is not None:
 assert a.product=='kuberoot-health-52db7b1' and isinstance(health_checks,list) and health_checks
 metrics['application_health_checks']=health_checks
metrics.update({'target_bundle_sha256':a.bundle_sha256,'target_version':a.target_version,'baseline_root_digest':os.environ['IMAGE_DIGEST']})
def typed(args):
 print('$ ',end='',flush=True)
 for ch in shlex.join([str(v) for v in args]):print(ch,end='',flush=True);time.sleep(.01)
 print(flush=True)
def nk(*args,visible=False):
 cmd=['kubectl','--request-timeout=5s',*args]
 if visible:typed(cmd)
 r=subprocess.run(cmd,text=True,capture_output=True,timeout=8,env=node_env)
 if visible:print(r.stdout+r.stderr,end='',flush=True)
 if r.returncode:raise RuntimeError(r.stderr)
 return r.stdout
def gateway(cmd,check=True):return subprocess.run([G,cmd],text=True,capture_output=True,timeout=30,check=check)
def slot_snapshot():return json.loads(nk('get','bootentries','-o','json'))
def stable_slots(obj):return sorted([{'name':s['metadata']['name'],'status':s['status']} for s in obj['items']],key=lambda s:s['name'])
try:
 subprocess.run(['bash','-c','source "$1/lib.sh"; vm_start 1','_',str(scripts)],env=env,check=True)
 for i in range(150):
  try:
   before=slot_snapshot();booted=next(x for x in before['items'] if x['status'].get('booted'))
   w=gateway('curl --fail --max-time 2 http://10.78.0.11:30080/http >/dev/null && curl --fail --max-time 2 http://10.78.0.11:30081/http >/dev/null',check=False)
   if booted['status']['state']=='Good' and w.returncode==0:break
  except Exception:pass
  time.sleep(2)
 else:raise RuntimeError('golden slot and cached W did not become ready')
 subprocess.run(['bash',str(scripts/'prepare-observer.sh')],env=env,check=True)
 (d/'bootentries-before.json').write_text(json.dumps(before,indent=2));original=booted['status']['release'];metrics['original_version']=original
 typed(['export','KUBECONFIG='+node_env['KUBECONFIG']]);nk('get','bootentries',visible=True)
 gateway(f'nohup python3 /home/observer/observer.py --http http://10.78.0.11:30080 --append http://10.78.0.11:30081 --kubeconfig /home/observer/kubeconfig --seconds {int(a.deadline+400)} --stop-file /home/observer/{name}.stop --output /home/observer/{name}.jsonl >/home/observer/{name}.log 2>&1 </dev/null &');observer_started=True
 time.sleep(10)
 serial=shlex.join(['python3',str(scripts/'serial-follow.py'),str(d/'serial.log'),'--from-end','--stop-file',str(d/'serial.stop'),'--max-seconds',str(a.deadline+120)])
 recorder=subprocess.Popen(['asciinema','rec','--cols','110','--rows','30','-q','-c',serial,str(d/'serial.cast')],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
 request={'apiVersion':'node.kuberoot.dev/v1alpha1','kind':'Upgrade','metadata':{'name':'field-bad-release'},'spec':{'url':a.bundle_url,'sha256':a.bundle_sha256,'reboot':not negative}}
 if health_checks is not None:request['spec']['healthChecks']=health_checks
 (d/'upgrade-request.json').write_text(json.dumps(request,indent=2));typed(['date','-u']);subprocess.run(['date','-u'],check=True)
 start=time.monotonic();fault_epoch=time.time();nk('create','-f',str(d/'upgrade-request.json'),visible=True)
 metrics['management_client']='one-shot kubectl create exited before reboot; no further upgrade/recovery commands; WAN available'
 metrics['upgrade_request_completed_utc']=datetime.datetime.now(datetime.timezone.utc).isoformat()
 seen_target=False;last_print=None;finished=False
 with (d/'observations.jsonl').open('x') as out:
  while time.monotonic()-start<a.deadline:
   row={'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'elapsed_seconds':time.monotonic()-start,'pvc_free_bytes':shutil.disk_usage(root).free}
   try:
    slots=slot_snapshot();row['bootentries']=slots;current=next(s for s in slots['items'] if s['status'].get('booted'))['status'];row['booted']=current
    if current['release']==a.target_version:
     seen_target=True
     if not negative and not control and not fault_verified:
      try:
       fault_log=nk('get','--raw','/apis/node.kuberoot.dev/v1alpha1/nodeservices/field-test-fault/log')
       (d/'fault-service.log').write_text(fault_log)
       expected={'init-crash':'intentional init-service crash','pod-network':'injected pod-network drop rules','wrong-content':'injected wrong-content NAT rule','incompatible-state':'test state migrated incompatibly'}[a.case]
       if expected in fault_log:
        if a.case=='wrong-content':
         probe=gateway('curl --silent --max-time 2 http://10.78.0.11:30080/http',check=False)
         fault_verified=probe.returncode==0 and probe.stdout=='X'*1024
        elif a.case=='pod-network':
         api=subprocess.run(['kubectl','--request-timeout=3s','get','--raw','/readyz'],env=env,text=True,capture_output=True,timeout=5)
         probe=gateway('curl --fail --max-time 2 http://10.78.0.11:30080/http',check=False)
         fault_verified=api.returncode==0 and probe.returncode!=0
        else:fault_verified=True
       if fault_verified:print(json.dumps({'utc':row['utc'],'fault_verified':a.case}),flush=True)
      except Exception as exc:row['fault_verification_error']=str(exc)
     row['fault_verified']=fault_verified
    try:row['upgrade']=json.loads(nk('get','upgrade','field-bad-release','-o','json'))
    except Exception as exc:row['upgrade_error']=str(exc)
    phase=row.get('upgrade',{}).get('status',{}).get('phase')
    signature=(current['release'],current['state'],current.get('triesLeft'),phase)
    if signature!=last_print:print(json.dumps({'utc':row['utc'],'elapsed_seconds':row['elapsed_seconds'],'booted':current,'upgrade_phase':phase}),flush=True);last_print=signature
    if negative and phase=='Failed':
     metrics['rejection_message']=row['upgrade']['status'].get('message');metrics['boot_selection_unchanged']=stable_slots(before)==stable_slots(slots);finished=True
     if not metrics['boot_selection_unchanged']:errors.append('boot selection changed for rejected bundle')
     if 'signature does not match any trusted release key' not in (metrics['rejection_message'] or ''):errors.append('failure was not the expected cryptographic trust rejection')
    elif not negative and not seen_target and phase=='Failed':
     metrics['preboot_failure_message']=row['upgrade']['status'].get('message');errors.append('upgrade failed before target boot; intended fault not reached');finished=True
    elif negative and phase in ['Writing','Staged']:
     errors.append('untrusted/tampered bundle reached write/selection');finished=True
    elif seen_target and current['release']==original and current['state']=='Good':
     metrics['automatic_rollback_seconds']=row['elapsed_seconds'];finished=True
     if control:errors.append('healthy control unexpectedly rolled back')
    elif seen_target and current['release']==a.target_version and current['state']=='Good' and row['elapsed_seconds']>=120:
     if control:metrics['healthy_release_marked_good']=True
     else:metrics['faulty_release_marked_good']=True;errors.append('faulty release marked Good instead of automatic rollback')
     finished=True
    if finished:(d/'bootentries-after.json').write_text(json.dumps(slots,indent=2))
   except Exception as exc:row['error']=str(exc)
   out.write(json.dumps(row)+'\n');out.flush();os.fsync(out.fileno())
   if finished:break
   time.sleep(5)
 metrics['target_boot_observed']=seen_target;metrics['fault_verified']=fault_verified
 if not negative and not control and not fault_verified:errors.append('injected fault was not verified; no product conclusion')
 if not finished:errors.append('no terminal rejection/rollback within configured deadline')
 settle_metrics,settle_errors=wait_for_service(gateway,name);metrics.update(settle_metrics);errors.extend(settle_errors)
 gateway(f'touch /home/observer/{name}.stop');time.sleep(4)
 (d/'observer.jsonl').write_text(gateway(f'cat /home/observer/{name}.jsonl').stdout)
 service_metrics,service_errors=evaluate_service(d/'observer.jsonl');metrics.update(service_metrics);errors.extend(service_errors)
 with (d/'records').open('wb') as f:subprocess.run([G,'curl --fail --max-time 20 http://10.78.0.11:30081/records'],stdout=f,timeout=30,check=True)
 check=subprocess.run(['python3',str(scripts/'check-records.py'),str(d/'observer.jsonl'),str(d/'records')],text=True,capture_output=True)
 metrics.update(json.loads(check.stdout))
 if check.returncode:errors.append('acknowledged record loss or incompatible persistent records')
 observed=[json.loads(x) for x in (d/'observer.jsonl').read_text().splitlines()]
 metrics['driver_overloads']=sum(r.get('error')=='driver_overload' for r in observed)
 if metrics['driver_overloads']:errors.append('observer overload; trial invalid')
 metrics['http_failed']=sum(r['kind']=='http' and not r.get('ok') for r in observed);metrics['append_failed']=sum(r['kind']=='append' and not r.get('ok') for r in observed)
 metrics['wrong_http_responses']=sum(r['kind']=='http' and r.get('error')=='unexpected response' for r in observed)
 typed(['date','-u']);subprocess.run(['date','-u'],check=True);nk('get','bootentries',visible=True)
except Exception as exc:errors.append(str(exc))
finally:
 subprocess.run(['bash',str(scripts/'wan-impairment.sh'),'clean'],check=False)
 if observer_started:
  try:gateway(f'touch /home/observer/{name}.stop',check=False)
  except Exception:pass
 try:
  counters=command(str(d/'qmp.sock'),'query-blockstats');(d/'block-counters.json').write_text(json.dumps(counters,indent=2))
  metrics['unexpected_block_io_failures']=sum(int(x.get('stats',{}).get(k,0)) for x in counters for k in ['failed_wr_operations','failed_rd_operations','failed_flush_operations'])
  if metrics['unexpected_block_io_failures']:errors.append('unexpected QEMU backend I/O errors; trial invalid')
 except Exception as exc:metrics['block_counters_error']=str(exc)
 try:command(str(d/'qmp.sock'),'quit')
 except Exception:pass
 if recorder:
  (d/'serial.stop').touch();recorder.wait(timeout=10)
  timing=json.loads(subprocess.check_output(['python3','/work/scripts/render.py',str(d/'serial.cast')],text=True))
  subprocess.run(['python3',str(scripts/'bound-cast.py'),str(d/'serial.retimed.cast')],check=True,stdout=(d/'serial-speed.json').open('w'))
  subprocess.run(['/work/bin/agg','-q','--font-size','16','--theme','monokai','--last-frame-duration',str(max(5,15.1-timing['playback_seconds'])),str(d/'serial.retimed.cast'),str(d/'serial.gif')],check=True)
record={'measurement_protocol':'upgrade-client-disconnect-v2','scenario_id':'E2','product':a.product,'source_commit':os.environ['SOURCE_COMMIT'],'image_digest':os.environ['IMAGE_DIGEST'],'repetition':a.repetition,'fault_seed':None,'fault_phase':a.case,'vcpu':2,'ram_mib':4096,'topology':'fresh raw/UEFI; real signed bundle; cached W; external gateway observer','metrics':metrics,'outage_seconds':None,'recovery_seconds':metrics.get('automatic_rollback_seconds'),'acknowledged_record_loss':metrics.get('acknowledged_record_loss'),'manual_actions':[],'status':'invalid-fixture' if (not negative and not control and not fault_verified) or metrics.get('driver_overloads') or metrics.get('unexpected_block_io_failures') else 'failed' if errors else 'passed','reason':'; '.join(errors) if errors else 'observed healthy control accepted and retained acknowledged records' if control else 'observed rejection/automatic rollback and retained acknowledged records','time_utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'artifact_paths':[str(d)]}
(d/'result.json').write_text(json.dumps(record,indent=2));print(json.dumps(record),flush=True)
