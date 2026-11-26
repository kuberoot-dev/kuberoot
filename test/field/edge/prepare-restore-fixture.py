#!/usr/bin/env python3
"""Make an encrypted S3 control-plane snapshot and separate application snapshot.
Run only after E7, on a fresh disposable clone of the kuberoot main W golden.
All secret manifests/URLs remain private and are excluded from publication.
"""
import base64,hashlib,json,os,pathlib,secrets,shlex,subprocess,time
root=pathlib.Path('/work/field-edge');s=pathlib.Path(__file__).resolve().parent;d=root/'restore-fixture';d.mkdir();private=d/'private';private.mkdir(mode=0o700);golden=root/'vm-main';vm=d/'source';vm.mkdir()
env=dict(os.environ,VM_DIR=str(vm));node_env=dict(env,KUBECONFIG=os.environ['NODE_KUBECONFIG']);G=str(s/'gateway-command.sh')
for f in ['system.raw','vars.fd']:subprocess.run(['cp','--sparse=always',str(golden/f),str(vm/f)],check=True)
def kube(*args,node=False,body=None):
 r=subprocess.run(['kubectl','--request-timeout=10s',*args],env=node_env if node else env,input=body,text=True,capture_output=True,timeout=15)
 if r.returncode:raise RuntimeError('kubectl operation failed; inspect private preparation log')
 return r.stdout
def private_json(name,obj):
 p=private/name;p.touch(mode=0o600);p.write_text(json.dumps(obj));return p
subprocess.run(['bash','-c','source "$1/lib.sh"; vm_start 1','_',str(s)],env=env,check=True)
for _ in range(150):
 try:
  kube('get','--raw','/readyz');break
 except Exception:time.sleep(2)
else:raise SystemExit('Source clone unavailable')
subprocess.run(['bash',str(s/'prepare-observer.sh')],env=env,check=True)
subprocess.run(['age-keygen','-o',str(private/'age.key')],check=True,stderr=(d/'age-public.txt').open('w'))
subprocess.run(['age-keygen','-o',str(private/'wrong-age.key')],check=True,stderr=subprocess.DEVNULL)
recipient=subprocess.check_output(['age-keygen','-y',str(private/'age.key')],text=True).strip()
credentials={'access_key':'field-restore','secret_key':secrets.token_urlsafe(32),'region':'us-east-1'};private_json('s3-credentials.json',credentials)
(d/'s3/backups').mkdir(parents=True)
server=subprocess.Popen(['rclone','serve','s3',str(d/'s3'),'--addr','0.0.0.0:19000','--auth-key',credentials['access_key']+','+credentials['secret_key']],stdout=(private/'s3.log').open('w'),stderr=subprocess.STDOUT,start_new_session=True)
(d/'s3.pid').write_text(str(server.pid));time.sleep(2);assert server.poll() is None
secret={'apiVersion':'v1','kind':'Secret','metadata':{'name':'kuberoot-state-backup','namespace':'kube-system'},'stringData':{'encryptionRecipient':recipient,'s3Endpoint':'http://10.0.2.2:19000','s3Bucket':'backups','s3AccessKey':credentials['access_key'],'s3SecretKey':credentials['secret_key']}}
secret_path=private_json('backup-secret.json',secret);kube('apply','-f',str(secret_path))
kube('create','configmap','field-restore-marker','--from-literal=value=before-snapshot')
# A pre-loss, unused, still-valid enrollment credential is deliberately saved.
# This tests replay after recovery; it does not claim to revoke all copied keys.
ticket={'apiVersion':'node.kuberoot.dev/v1alpha1','kind':'JoinTicket','metadata':{'name':'field-preloss-ticket'},'spec':{'nodeName':'kuberoot-780002','role':'Worker','address':'10.78.0.12','ttl':'24h'}}
kube('create','-f','-',node=True,body=json.dumps(ticket))
for _ in range(60):
 membership=kube('get','jointicket','field-preloss-ticket','-o','jsonpath={.status.membership}',node=True)
 if membership:
  p=private/'preloss-membership.yaml';p.touch(mode=0o600);p.write_text(membership);break
 time.sleep(1)
else:raise SystemExit('No pre-loss enrollment credential issued')
# Establish durable records through the same external observer used by W.
subprocess.run([G,'python3 /home/observer/observer.py --http http://10.78.0.11:30080 --append http://10.78.0.11:30081 --kubeconfig /home/observer/kubeconfig --seconds 60 --output /home/observer/E8-before-snapshot.jsonl'],check=True)
(d/'observer-before.jsonl').write_text(subprocess.check_output([G,'cat /home/observer/E8-before-snapshot.jsonl'],text=True))
with (d/'application-records').open('wb') as f:subprocess.run([G,'curl --fail --max-time 20 http://10.78.0.11:30081/records'],stdout=f,check=True)
subprocess.run(['python3',str(s/'check-records.py'),str(d/'observer-before.jsonl'),str(d/'application-records')],check=True,stdout=(d/'application-durability.json').open('w'))
# App snapshot has its own encryption/RPO, independent of StateBackup scope.
subprocess.run(['age','-r',recipient,'-o',str(d/'s3/backups/application-records.age'),str(d/'application-records')],check=True)
kube('create','-f','-',node=True,body=json.dumps({'apiVersion':'node.kuberoot.dev/v1alpha1','kind':'StateBackup','metadata':{'name':'field-site-loss'}}))
for _ in range(120):
 backup=json.loads(kube('get','statebackup','field-site-loss','-o','json',node=True));status=backup.get('status',{})
 if status.get('location','').startswith('s3://'):break
 time.sleep(2)
else:raise SystemExit('Encrypted StateBackup upload did not finish')
assert status['encrypted'];(d/'backup-status.json').write_text(json.dumps(status,indent=2))
source=d/'s3'/status['location'].removeprefix('s3://');assert hashlib.file_digest(source.open('rb'),'sha256').hexdigest()==status['sha256']
truncated=d/'s3/backups/truncated.age';data=source.read_bytes();truncated.write_bytes(data[:len(data)//2])
kube('patch','configmap','field-restore-marker','--type','merge','-p','{"data":{"value":"after-snapshot"}}')
(d/'source-node.json').write_text(kube('get','nodes','-o','json'))
subprocess.run(['bash',str(s/'stop-dut.sh'),str(vm)],env=env,check=True)
# Original source is quarantined and never passed to a restore attempt.
subprocess.run(['bash',str(s/'preserve-failed-disk.sh'),str(vm/'system.raw'),str(golden)],check=True)
subprocess.run(['mkimage','-arch','amd64','-version','0.1.0-field-main.1b29543','-artifacts',str(root/'build-main/out/amd64'),'-trust',str(root/'kuberoot-main/trust'),'-out',str(d/'restore-media.img'),'-args','kuberoot.nameserver=10.78.0.1'],check=True)
(d/'prepared').write_text('Source unavailable; encrypted S3 state and independent app snapshot verified.\n')
print(json.dumps({'source_quarantined':True,'state_backup_bytes':source.stat().st_size,'state_backup_sha256':status['sha256'],'application_bytes':len((d/'application-records').read_bytes()),'app_snapshot_separate':True}),flush=True)
