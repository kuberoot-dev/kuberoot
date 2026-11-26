#!/usr/bin/env python3
"""External quorum assertions on three real APIs; QMP isolates only peer LAN."""
import argparse,concurrent.futures,datetime,json,os,pathlib,shlex,subprocess,time
from qmp import command
p=argparse.ArgumentParser();p.add_argument('--ha-root',type=pathlib.Path,required=True);p.add_argument('--product',required=True);p.add_argument('--leader',type=int,choices=[1,2,3]);a=p.parse_args()
scripts=pathlib.Path(__file__).resolve().parent;d=a.ha_root/'E5';d.mkdir();env=dict(os.environ,HA_ROOT=str(a.ha_root));errors=[];rows=[];acked={};metrics={};leader=a.leader;recorder=None;baseline_valid=False
assert str(a.ha_root).startswith('/work/field-edge/ha-')
def stamp():return datetime.datetime.now(datetime.timezone.utc).isoformat()
def typed(args):
 print('$ ',end='',flush=True)
 for ch in shlex.join([str(v) for v in args]):print(ch,end='',flush=True);time.sleep(.012)
 print(flush=True)
def kube(i,args,body=None):
 e=dict(env,KUBECONFIG=str(a.ha_root/f'kubeconfig-{i}'))
 try:return subprocess.run(['kubectl','--request-timeout=2s',*args],input=body,capture_output=True,text=True,timeout=4,env=e)
 except subprocess.TimeoutExpired as exc:return subprocess.CompletedProcess(exc.cmd,124,'',str(exc))
def link(i,up):
 args=['python3',str(scripts/'qmp.py'),str(a.ha_root/f'n{i}/qmp.sock'),'set_link',json.dumps({'name':'peer-nic','up':up})]
 typed(args);subprocess.run(args,check=True)
def one(i,phase,n):
 name=f'quorum-{phase}-{n}-{i}';value=f'{time.time_ns()}-{i}'
 obj={'apiVersion':'v1','kind':'ConfigMap','metadata':{'name':name,'namespace':'default'},'data':{'value':value}}
 start=time.monotonic();r=kube(i,['create','-f','-','-o','json'],json.dumps(obj))
 row={'utc':stamp(),'phase':phase,'node':i,'name':name,'value':value,'ok':r.returncode==0,'elapsed_seconds':time.monotonic()-start,'stderr':r.stderr}
 if r.returncode==0:
  created=json.loads(r.stdout);row['resourceVersion']=created['metadata']['resourceVersion']
 return row
def phase(name,seconds,allowed):
 start=time.monotonic();first={};n=0
 typed(['date','-u']);subprocess.run(['date','-u'],check=True)
 print(json.dumps({'phase':name,'expected_committers':allowed,'utc':stamp()}),flush=True)
 with concurrent.futures.ThreadPoolExecutor(max_workers=3) as pool:
  while time.monotonic()-start<seconds:
   batch=list(pool.map(lambda i:one(i,name,n),[1,2,3]));n+=1
   for row in batch:
    row['phase_elapsed_seconds']=time.monotonic()-start;rows.append(row)
    with (d/'probes.jsonl').open('a') as f:f.write(json.dumps(row)+'\n');f.flush();os.fsync(f.fileno())
    if row['ok']:
     acked[row['name']]=row['value'];first.setdefault(row['node'],row['phase_elapsed_seconds'])
     if row['node'] not in allowed:errors.append(f"{name}: node {row['node']} committed outside majority")
   print(json.dumps({'utc':stamp(),'phase':name,'elapsed_seconds':time.monotonic()-start,'committed':[r['node'] for r in batch if r['ok']]}),flush=True)
   time.sleep(1)
 metrics[name]={'first_commit_seconds':first,'rounds':n,'commits':sum(r['ok'] for r in rows if r['phase']==name)}
 for i in allowed:
  if i not in first:errors.append(f'{name}: expected node {i} never committed')
  elif first[i]>30:errors.append(f'{name}: node {i} majority recovery exceeded 30 seconds')
def agreement(label):
 start=time.monotonic();last={}
 while time.monotonic()-start<120:
  for i in [1,2,3]:
   r=kube(i,['get','configmaps','-n','default','-o','json'])
   if r.returncode:last[i]={'error':r.stderr};continue
   objects={x['metadata']['name']:x.get('data',{}).get('value') for x in json.loads(r.stdout)['items']}
   last[i]={'missing_or_wrong':[name for name,value in acked.items() if objects.get(name)!=value]}
  if len(last)==3 and all(v.get('missing_or_wrong')==[] for v in last.values()):
   metrics[label+'_agreement_seconds']=time.monotonic()-start;break
  time.sleep(2)
 else:errors.append(label+': acknowledged marker agreement missing after 120 seconds')
 (d/(label+'-agreement.json')).write_text(json.dumps(last,indent=2))
try:
 if leader is None:
  m=json.loads((a.ha_root/'members-before.json').read_text())
  peer=next(x for x in m['items'] if x['status'].get('leader'))['status']['peerURLs'][0]
  leader=next(i for i in [1,2,3] if f'10.79.0.1{i}:' in peer)
 metrics['isolated_leader']=leader
 serial_command=shlex.join(['python3',str(scripts/'serial-follow.py'),str(a.ha_root/f'n{leader}/serial.log'),'--from-end','--stop-file',str(d/'serial.stop')])
 recorder=subprocess.Popen(['asciinema','rec','--cols','110','--rows','30','-q','-c',serial_command,str(d/'serial.cast')],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
 for i in [1,2,3]:
  typed(['export','KUBECONFIG='+str(a.ha_root/f'kubeconfig-{i}')]);typed(['kubectl','get','nodes','-o','wide'])
  r=kube(i,['get','nodes','-o','wide']);print(r.stdout+r.stderr,flush=True)
 phase('baseline',15,[1,2,3])
 if errors:raise RuntimeError('baseline invalid; no partition injected')
 baseline_valid=True
 link(leader,False)
 phase('two-plus-one',60,[i for i in [1,2,3] if i!=leader])
 # Reboot the isolated former leader; peer link stays down from early firmware.
 serial=a.ha_root/f'n{leader}/serial.log';(d/'isolated-leader-before-reboot.log').write_bytes(serial.read_bytes())
 typed(['python3',str(scripts/'qmp.py'),str(a.ha_root/f'n{leader}/qmp.sock'),'quit'])
 command(str(a.ha_root/f'n{leader}/qmp.sock'),'quit');time.sleep(2)
 subprocess.run(['bash',str(scripts/'ha-vm.sh'),'start',str(leader)],env=env,check=True)
 link(leader,False)
 phase('isolated-leader-reboot',60,[i for i in [1,2,3] if i!=leader])
 (d/'isolated-leader-recovery.log').write_bytes(serial.read_bytes())
 for i in [1,2,3]:link(i,True)
 agreement('first-heal')
 phase('healed',15,[1,2,3])
 for i in [1,2,3]:link(i,False)
 phase('one-plus-one-plus-one',60,[])
 for i in [1,2,3]:link(i,True)
 agreement('final-heal')
 phase('final',15,[1,2,3])
except Exception as exc:errors.append(str(exc))
finally:
 for i in [1,2,3]:
  try:link(i,True)
  except Exception as exc:errors.append('heal cleanup: '+str(exc))
 if recorder:
  (d/'serial.stop').touch();recorder.wait(timeout=10)
  subprocess.run(['python3',str(scripts/'render-serial.py'),str(d/'serial.cast')],check=True)
record={'scenario_id':'E5','product':a.product,'source_commit':os.environ.get('SOURCE_COMMIT'),'image_digest':os.environ.get('IMAGE_DIGEST'),'vcpu':2,'ram_mib':4096,'node_count':3,'topology':'3 real etcd control planes, 12 GiB guest RAM; dedicated multicast peer LAN; separate user-net management; shared pod 6 CPU limit','fault_seed':None,'fault_phase':'2+1, isolated leader reboot, heal, 1+1+1, heal','status':'invalid-fixture' if not baseline_valid else 'failed' if errors else 'passed','reason':'; '.join(errors) if errors else 'only majority committed; acknowledged values agreed after heal','metrics':metrics,'manual_actions':[],'acknowledged_record_loss':None,'outage_seconds':None,'recovery_seconds':metrics.get('final-heal_agreement_seconds'),'time_utc':stamp(),'artifact_paths':[str(d)],'measurement_notes':['ConfigMap quorum assertions are distinct from application storage replication and W durability.']}
(d/'result.json').write_text(json.dumps(record,indent=2));print(json.dumps(record),flush=True)
