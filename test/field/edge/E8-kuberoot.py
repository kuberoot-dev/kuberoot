#!/usr/bin/env python3
"""Real S3 restore on blank disk, plus damaging-input rejection on a good disk.
Application data is restored separately; all private restore manifests stay local.
"""
import argparse,datetime,hashlib,importlib.util,json,os,pathlib,re,shlex,subprocess,time
from qmp import command
p=argparse.ArgumentParser();p.add_argument('--case',choices=['valid','wrong-key','truncated','s3-down'],required=True);p.add_argument('--output',type=pathlib.Path,required=True);a=p.parse_args()
root=pathlib.Path('/work/field-edge');s=pathlib.Path(__file__).resolve().parent;fixture=root/'restore-fixture';d=a.output;assert d.is_relative_to(root);assert (fixture/'prepared').exists();d.mkdir();private=d/'private';private.mkdir(mode=0o700)
spec=importlib.util.spec_from_file_location('s3_presign',s/'s3-presign.py');presigner=importlib.util.module_from_spec(spec);spec.loader.exec_module(presigner)
node_env=dict(os.environ,KUBECONFIG=os.environ['NODE_KUBECONFIG']);env=dict(os.environ,VM_DIR=str(d));G=str(s/'gateway-command.sh');errors=[];metrics={};recorder=None;start=None;negative=a.case!='valid';accepted=False

def safe(text):
 text=re.sub(r'(https?://[^\s"\']+)\?[^\s"\']+',r'\1?[private signed query]',text)
 return re.sub(r'AGE-SECRET-KEY-[A-Z0-9]+','[private age identity]',text)
def typed(args):
 print('$ ',end='',flush=True)
 for ch in shlex.join([str(x) for x in args]):print(ch,end='',flush=True);time.sleep(.01)
 print(flush=True)
def kube(*args,node=False,visible=False,body=None,timeout=10):
 cmd=['kubectl','--request-timeout='+str(timeout)+'s',*args]
 if visible:typed(cmd)
 r=subprocess.run(cmd,env=node_env if node else env,input=body,text=True,capture_output=True,timeout=timeout+5)
 if visible:print(safe(r.stdout+r.stderr),end='',flush=True)
 return r
def gateway(cmd):return subprocess.run([G,cmd],text=True,capture_output=True,timeout=30)
def boot(media=None):
 e=dict(env)
 if media:e['SEED_MEDIA']=str(media)
 else:e.pop('SEED_MEDIA',None)
 subprocess.run(['bash',str(s/'boot-node.sh'),'1'],env=e,check=True)
def stop():
 try:command(str(d/'qmp.sock'),'quit')
 except Exception:pass
 time.sleep(2)
def digest():return json.loads(subprocess.check_output(['python3',str(s/'boot-partition-digests.py'),str(d/'system.raw')],text=True))
try:
 if negative:
  subprocess.run(['cp','--sparse=always',str(root/'vm-main/system.raw'),str(d/'system.raw')],check=True)
  (d/'boot-partitions-before.json').write_text(json.dumps(digest(),indent=2))
 else:subprocess.run(['qemu-img','create','-f','raw',str(d/'system.raw'),'32G'],check=True)
 # Fresh firmware boots the live media; system disk is vdb during installation.
 subprocess.run(['cp','/usr/share/OVMF/OVMF_VARS_4M.fd',str(d/'vars.fd')],check=True)
 boot(fixture/'restore-media.img')
 for _ in range(150):
  if kube('get','disks',node=True).returncode==0:break
  time.sleep(2)
 else:raise RuntimeError('Live node API unavailable')
 typed(['export','KUBECONFIG='+node_env['KUBECONFIG']]);kube('get','disks',node=True,visible=True)
 serial=shlex.join(['python3',str(s/'serial-follow.py'),str(d/'serial.log'),'--from-end','--stop-file',str(d/'serial.stop'),'--max-seconds','1500'])
 serial+=' | '+shlex.join(['python3',str(s/'redact-stream.py')])
 recorder=subprocess.Popen(['asciinema','rec','--cols','110','--rows','30','-q','-c',serial,str(d/'serial.cast')],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
 status=json.loads((fixture/'backup-status.json').read_text());location=status['location'].removeprefix('s3://');bucket,key=location.split('/',1)
 if a.case=='truncated':bucket,key='backups','truncated.age'
 credentials=json.loads((fixture/'private/s3-credentials.json').read_text())
 endpoint='http://10.0.2.2:19001' if a.case=='s3-down' else 'http://10.0.2.2:19000'
 # Closed dedicated port models an unavailable S3 endpoint, leaving the archive intact.
 url=presigner.presign(endpoint,bucket,key,credentials)
 identity=next(line for line in (fixture/'private'/('wrong-age.key' if a.case=='wrong-key' else 'age.key')).read_text().splitlines() if line.startswith('AGE-SECRET-KEY-'))
 restore={'url':url,'sha256':status['sha256'],'identity':identity}
 request={'apiVersion':'node.kuberoot.dev/v1alpha1','kind':'Installation','metadata':{'name':'field-site-restore'},'spec':{'disk':'vdb','reboot':True,'restore':restore}}
 manifest=private/'restore.json';manifest.touch(mode=0o600);manifest.write_text(json.dumps(request))
 typed(['date','-u']);subprocess.run(['date','-u']);start=time.monotonic();metrics['restore_started_utc']=datetime.datetime.now(datetime.timezone.utc).isoformat()
 result=kube('create','-f',str(manifest),node=True,visible=True);accepted=result.returncode==0
 if not accepted:raise RuntimeError('Installation creation failed: '+safe(result.stderr))
 completed=False
 with (d/'restore-status.jsonl').open('x') as out:
  while time.monotonic()-start<900:
   r=kube('get','installation','field-site-restore','-o','json',node=True)
   if r.returncode==0:
    obj=json.loads(r.stdout);row={'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'elapsed_seconds':time.monotonic()-start,'status':json.loads(safe(json.dumps(obj.get('status',{}))))}
    out.write(json.dumps(row)+'\n');out.flush();print(json.dumps(row),flush=True)
    if obj.get('status',{}).get('phase')=='Failed':
     metrics['restore_rejected']=True;metrics['rejection_status']=row['status'];completed=True;break
   if 'reboot: Restarting system' in (d/'serial.log').read_text(errors='replace'):
    metrics['installer_reboot_observed']=True;completed=True;break
   time.sleep(3)
  if not completed:errors.append('no terminal restore outcome within 15 minutes')
 if negative:
  if not metrics.get('restore_rejected'):errors.append('invalid restore input was not explicitly rejected')
  stop();after=digest();(d/'boot-partitions-after.json').write_text(json.dumps(after,indent=2))
  metrics['existing_boot_partitions_unchanged']=after==json.loads((d/'boot-partitions-before.json').read_text())
  if not metrics['existing_boot_partitions_unchanged']:errors.append('failed restore modified existing boot partitions')
  # Prove the prior installation still works, not just that a status said Failed.
  subprocess.run(['cp',str(root/'vm-main/vars.fd'),str(d/'vars.fd')],check=True);boot()
 else:
  if metrics.get('restore_rejected'):errors.append('valid encrypted S3 restore rejected')
  stop();(d/'live-serial.log').write_bytes((d/'serial.log').read_bytes());boot()
 deadline=time.monotonic()+300
 while time.monotonic()<deadline:
  r=kube('get','--raw','/readyz')
  if r.returncode==0:
   nodes=kube('get','nodes','-o','json')
   if nodes.returncode==0 and any(c['type']=='Ready' and c['status']=='True' for n in json.loads(nodes.stdout)['items'] for c in n['status']['conditions']):break
  time.sleep(3)
 else:raise RuntimeError('installed/recovered cluster did not become Ready')
 metrics['ready_seconds_from_restore_request']=time.monotonic()-start
 typed(['export','KUBECONFIG='+env['KUBECONFIG']]);kube('get','nodes','-o','wide',visible=True)
 if negative:
  r=gateway('curl --fail --max-time 5 http://10.78.0.11:30080/http');metrics['existing_W_healthy']=r.returncode==0 and r.stdout=='W'*1024
  if not metrics['existing_W_healthy']:errors.append('prior W did not survive rejected restoration')
 else:
  marker=kube('get','configmap','field-restore-marker','-o','jsonpath={.data.value}',visible=True)
  metrics['marker_as_of_snapshot']=marker.stdout=='before-snapshot'
  if not metrics['marker_as_of_snapshot']:errors.append('control-plane marker did not match snapshot')
  before_node=json.loads((fixture/'source-node.json').read_text())['items'][0]
  after_node=json.loads(kube('get','nodes','-o','json').stdout)['items'][0]
  metrics['identity_restored']=after_node['metadata']['name']==before_node['metadata']['name']
  metrics['fresh_boot_id']=after_node['status']['nodeInfo']['bootID']!=before_node['status']['nodeInfo']['bootID']
  if not metrics['identity_restored'] or not metrics['fresh_boot_id']:errors.append('restored identity/fresh boot evidence missing')
  # StateBackup intentionally excludes application hostPath data. Restore its
  # separately encrypted, separately checked snapshot through the workload API.
  r=kube('-n','field','rollout','status','deployment/append','--timeout=240s',timeout=245)
  if r.returncode:raise RuntimeError('application image/startup unavailable after state restore')
  app=private/'application-records';subprocess.run(['age','-d','-i',str(fixture/'private/age.key'),'-o',str(app),str(fixture/'s3/backups/application-records.age')],check=True)
  code='import os,sys; data=sys.stdin.buffer.read(); f=open("/data/records","wb"); f.write(data); f.flush(); os.fsync(f.fileno()); f.close(); print("application snapshot restored",len(data),"bytes")'
  cmd=['kubectl','-n','field','exec','-i','deployment/append','--','python3','-c',code];typed(cmd)
  with app.open('rb') as f:subprocess.run(cmd,env=env,stdin=f,check=True,timeout=30)
  r=gateway('curl --fail --max-time 20 http://10.78.0.11:30081/records');(d/'records').write_text(r.stdout)
  check=subprocess.run(['python3',str(s/'check-records.py'),str(fixture/'observer-before.jsonl'),str(d/'records')],text=True,capture_output=True);metrics.update(json.loads(check.stdout))
  metrics['application_snapshot_bytes']=app.stat().st_size;metrics['application_restore_seconds']=time.monotonic()-start
  if check.returncode:errors.append('application snapshot acknowledgements missing/corrupt')
  if metrics['application_restore_seconds']>900:errors.append('full declared restore exceeded 15 minutes')
 if not negative:
  subprocess.run(['python3',str(s/'replay-preloss-membership.py')],env=dict(env,CASE_OUTPUT=str(d)),check=True)
  replay=json.loads((d/'credential-replay.json').read_text());metrics['credential_replay']=replay
  if not replay.get('clone_rejected'):errors.append('pre-loss credential clone rejection not established')
 typed(['date','-u']);subprocess.run(['date','-u'])
except Exception as exc:errors.append(safe(str(exc)))
finally:
 stop()
 for source,target in [('serial.log','serial-redacted.log'),('live-serial.log','live-serial-redacted.log')]:
  if (d/source).exists():(d/target).write_text(safe((d/source).read_text(errors='replace')))
 if recorder:
  (d/'serial.stop').touch();recorder.wait(timeout=15)
  timing=json.loads(subprocess.check_output(['python3','/work/scripts/render.py',str(d/'serial.cast')],text=True))
  subprocess.run(['python3',str(s/'bound-cast.py'),str(d/'serial.retimed.cast')],check=True,stdout=(d/'serial-speed.json').open('w'))
  subprocess.run(['agg','-q','--font-size','16','--theme','monokai','--last-frame-duration',str(max(5,15.1-timing['playback_seconds'])),str(d/'serial.retimed.cast'),str(d/'serial.gif')],check=True)
record={'scenario_id':'E8','product':os.environ['PRODUCT'],'source_commit':os.environ['SOURCE_COMMIT'],'image_digest':os.environ['IMAGE_DIGEST'],'fault_seed':None,'fault_phase':a.case,'vcpu':2,'ram_mib':4096,'topology':'S3 outside DUT; source disk quarantined; valid restore onto blank 32 GiB; negative inputs against a good installed clone','metrics':metrics,'outage_seconds':None,'recovery_seconds':metrics.get('application_restore_seconds',metrics.get('ready_seconds_from_restore_request')),'acknowledged_record_loss':metrics.get('acknowledged_record_loss'),'manual_actions':['create Installation from private S3 URL + age identity','remove live installer media and boot installed disk']+(['restore separate app snapshot through kubectl exec'] if not negative else []),'status':'invalid-fixture' if not accepted else 'failed' if errors else 'passed-subcase','reason':'; '.join(errors) if errors else 'restore/rejection and applicable credential-replay gates passed','time_utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'artifact_paths':[str(d)],'measurement_notes':['Original source disk is unavailable to the restore procedure but retained offline as forensic evidence.','Application image cache is not included in StateBackup; image retrieval/startup is included in measured restore duration.']}
(d/'result.json').write_text(json.dumps(record,indent=2));print(json.dumps(record),flush=True)
