#!/usr/bin/env python3
"""Talos native etcd snapshot reference, blank replacement and separate app RPO."""
import datetime,hashlib,importlib.util,json,os,pathlib,shlex,subprocess,time,urllib.request
from qmp import command
root=pathlib.Path('/work/field-edge');s=pathlib.Path(__file__).resolve().parent;golden=root/'talos';d=root/'results/E8-talos-v1.14.7-valid';d.mkdir();source=d/'source';source.mkdir();new=d/'replacement';new.mkdir();private=d/'private';private.mkdir(mode=0o700);fixture=root/'restore-fixture';errors=[];metrics={};recorder=None;start=None
base=['talosctl','--talosconfig',str(golden/'config/talosconfig'),'--endpoints','127.0.0.1:15000','--nodes','10.78.0.11'];env=dict(os.environ,KUBECONFIG=str(golden/'kubeconfig'));G=str(s/'gateway-command.sh')
def run(args,**kwargs):
 print('$ '+shlex.join([str(x) for x in args]),flush=True)
 return subprocess.run([str(x) for x in args],check=True,**kwargs)
def kube(*args):return subprocess.run(['kubectl','--request-timeout=10s',*args],env=env,text=True,capture_output=True,timeout=15)
def talos(*args,**kw):return run(base+list(args),**kw)
def boot(directory,iso=False):
 e=dict(env,VM_DIR=str(directory));e.pop('SEED_MEDIA',None)
 if iso:e['BOOT_ISO']=str(root/'downloads/talos-v1.14.7-metal-amd64.iso')
 run(['bash',s/'boot-node.sh','1'],env=e)
def stop(directory):
 try:command(str(directory/'qmp.sock'),'quit')
 except Exception:pass
 time.sleep(2)
def wait_api():
 for _ in range(180):
  if kube('get','--raw','/readyz').returncode==0:return
  time.sleep(3)
 raise RuntimeError('Kubernetes API did not return')
try:
 for f in ['system.raw','vars.fd']:run(['cp','--sparse=always',golden/f,source/f])
 boot(source);wait_api();run(['bash',s/'prepare-observer.sh'],env=env)
 r=kube('create','configmap','field-restore-marker','--from-literal=value=before-snapshot');assert r.returncode==0,r.stderr
 run([G,'python3 /home/observer/observer.py --http http://10.78.0.11:30080 --append http://10.78.0.11:30081 --kubeconfig /home/observer/kubeconfig --seconds 60 --output /home/observer/E8-talos-snapshot.jsonl'])
 with (d/'observer.jsonl').open('wb') as f:run([G,'cat /home/observer/E8-talos-snapshot.jsonl'],stdout=f)
 with (private/'app-records').open('wb') as f:run([G,'curl --fail --max-time 20 http://10.78.0.11:30081/records'],stdout=f)
 snapshot=fixture/'s3/backups/talos-etcd.snapshot';talos('etcd','snapshot',str(snapshot))
 metrics['snapshot_bytes']=snapshot.stat().st_size;metrics['snapshot_sha256']=hashlib.file_digest(snapshot.open('rb'),'sha256').hexdigest()
 metrics['snapshot_encryption']='native etcd snapshot is plaintext; private fixture S3 credentials restrict access'
 r=kube('patch','configmap','field-restore-marker','--type','merge','-p','{"data":{"value":"after-snapshot"}}');assert r.returncode==0
 before=json.loads(kube('get','nodes','-o','json').stdout)['items'][0];stop(source)
 run(['bash',s/'preserve-failed-disk.sh',source/'system.raw',golden])
 # Start timing with an empty disk; original disk remains unavailable.
 start=time.monotonic();run(['date','-u']);boot(new,iso=True)
 serial=shlex.join(['python3',str(s/'serial-follow.py'),str(new/'serial.log'),'--stop-file',str(d/'serial.stop'),'--max-seconds','1500'])
 recorder=subprocess.Popen(['asciinema','rec','--cols','110','--rows','30','-q','-c',serial,str(d/'serial.cast')],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
 # Native initial unconfigured machine API. No enrolled TLS verification bypass.
 for _ in range(150):
  r=subprocess.run(['talosctl','--nodes','127.0.0.1:15000','apply-config','--insecure','--file',str(golden/'config/controlplane-fixed.yaml')],capture_output=True,text=True,timeout=15)
  if r.returncode==0:break
  time.sleep(2)
 else:raise RuntimeError('blank Talos machine configuration failed')
 for _ in range(180):
  r=subprocess.run(base+['version'],capture_output=True,text=True,timeout=15)
  if r.returncode==0:break
  time.sleep(2)
 else:raise RuntimeError('authenticated machine API did not return')
 spec=importlib.util.spec_from_file_location('presign',s/'s3-presign.py');module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
 credentials=json.loads((fixture/'private/s3-credentials.json').read_text());url=module.presign('http://127.0.0.1:19000','backups','talos-etcd.snapshot',credentials)
 recovered=private/'downloaded.snapshot'
 with urllib.request.urlopen(url,timeout=30) as response:recovered.write_bytes(response.read())
 assert hashlib.file_digest(recovered.open('rb'),'sha256').hexdigest()==metrics['snapshot_sha256']
 talos('bootstrap','--recover-from',str(recovered));wait_api()
 r=kube('get','configmap','field-restore-marker','-o','jsonpath={.data.value}');metrics['marker_as_of_snapshot']=r.returncode==0 and r.stdout=='before-snapshot'
 after=json.loads(kube('get','nodes','-o','json').stdout)['items'][0];metrics['fresh_boot_id']=before['status']['nodeInfo']['bootID']!=after['status']['nodeInfo']['bootID'];metrics['kubernetes_ready_seconds']=time.monotonic()-start
 run(['kubectl','-n','field','rollout','status','deployment/append','--timeout=300s'],env=env)
 code='import sys,os; data=sys.stdin.buffer.read(); f=open("/data/records","wb"); f.write(data); f.flush(); os.fsync(f.fileno()); f.close(); print("Restored separate app snapshot",len(data))'
 with (private/'app-records').open('rb') as f:run(['kubectl','-n','field','exec','-i','deployment/append','--','python3','-c',code],env=env,stdin=f)
 with (d/'records').open('wb') as f:run([G,'curl --fail --max-time 20 http://10.78.0.11:30081/records'],stdout=f)
 r=subprocess.run(['python3',str(s/'check-records.py'),str(d/'observer.jsonl'),str(d/'records')],text=True,capture_output=True);metrics.update(json.loads(r.stdout))
 if r.returncode:errors.append('application snapshot records missing or corrupt')
 metrics['restore_seconds']=time.monotonic()-start
 if metrics['restore_seconds']>900:errors.append('snapshot and app restore exceeded 15 minutes')
 if not metrics['marker_as_of_snapshot'] or not metrics['fresh_boot_id']:errors.append('snapshot marker or fresh boot identity not established')
 run(['date','-u']);run(['kubectl','get','nodes','-o','wide'],env=env)
except Exception as exc:errors.append(str(exc))
finally:
 stop(source);stop(new)
 if recorder:
  (d/'serial.stop').touch();recorder.wait(timeout=15);run(['python3',s/'render-serial.py',d/'serial.cast'])
record={'scenario_id':'E8','product':'talos-v1.14.7','source_commit':os.environ['SOURCE_COMMIT'],'image_digest':os.environ['IMAGE_DIGEST'],'vcpu':2,'ram_mib':4096,'fault_seed':None,'fault_phase':'valid native etcd snapshot reference','topology':'single Talos node lost; blank replacement; native snapshot on external local S3; separate app snapshot','metrics':metrics,'recovery_seconds':metrics.get('restore_seconds'),'outage_seconds':None,'acknowledged_record_loss':metrics.get('acknowledged_record_loss'),'manual_actions':['native Talos etcd snapshot to external storage','native initial machine configuration','native bootstrap --recover-from with hash verification','separate app snapshot restore via Kubernetes API'],'status':'failed' if errors else 'passed-reference','reason':'; '.join(errors) if errors else 'native etcd snapshot reference and separate app RPO passed','measurement_notes':['Reference lane covers valid native etcd snapshot recovery; it is not kuberoot StateBackup and does not include application volumes.','Initial Talos maintenance API is unauthenticated by design; enrolled machine API uses existing trust.','No original source disk participates in restore. Snapshot and private configuration are never published.'],'artifact_paths':[str(d)],'time_utc':datetime.datetime.now(datetime.timezone.utc).isoformat()}
(d/'result.json').write_text(json.dumps(record,indent=2));print(json.dumps(record),flush=True)
