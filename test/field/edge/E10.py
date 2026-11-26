#!/usr/bin/env python3
"""Three real NAT sites: failed canary, datastore restart, leader loss and RBAC."""
import datetime,json,os,pathlib,shlex,signal,subprocess,time
from qmp import command
root=pathlib.Path('/work/field-edge');s=pathlib.Path(__file__).resolve().parent;f=root/'fleet-field';assert (f/'prepared').exists();d=root/'results/E10-kuberoot-main-1b29543';d.mkdir();env=dict(os.environ,KUBECONFIG=str(f/'center-kubeconfig'));errors=[];metrics={};recorders=[];stopped_supervisor=None;observers=[];start=None

def typed(args):
 print('$ ',end='',flush=True)
 for ch in shlex.join([str(x) for x in args]):print(ch,end='',flush=True);time.sleep(.01)
 print(flush=True)
def kube(*args,visible=False,config=None):
 cmd=['kubectl','--request-timeout=5s',*args];e=dict(env)
 if config:e['KUBECONFIG']=str(config)
 if visible:typed(['export','KUBECONFIG='+e['KUBECONFIG']]);typed(cmd)
 r=subprocess.run(cmd,env=e,text=True,capture_output=True,timeout=8)
 if visible:print(r.stdout+r.stderr,end='',flush=True)
 if r.returncode:raise RuntimeError(r.stderr)
 return r.stdout
def gateway(i,cmd):return subprocess.run([str(s/'fleet-gateway-command.sh'),str(i),cmd],text=True,capture_output=True,timeout=30)
def network(i,mode):
 typed(['bash',str(s/'fleet-wan-impairment.sh'),str(i),mode]);subprocess.run(['bash',str(s/'fleet-wan-impairment.sh'),str(i),mode],check=True)
def snapshot():
 return {'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'nodes':json.loads(kube('get','fleetnodes','-o','json')),'rollout':json.loads(kube('get','fleetrollout','field-failed-canary','-o','json')),'lease':json.loads(kube('-n','kube-system','get','lease','kuberoot-fleet','-o','json'))}
def leaseholder():return json.loads(kube('-n','kube-system','get','lease','kuberoot-fleet','-o','json'))['spec']['holderIdentity']
try:
 subprocess.run(['python3',str(s/'prepare-fleet-reader.py')],env=env,check=True)
 for i in [1,2,3]:
  e=dict(env,KUBECONFIG=str(f/f'site-{i}-kubeconfig'),GATEWAY_SSH_PORT=str(22300+i),GATEWAY_KNOWN_HOSTS=str(f/'gateway-known-hosts'))
  subprocess.run(['bash',str(s/'prepare-observer.sh')],env=e,check=True)
  name=f'E10-site-{i}';gateway(i,f'nohup python3 /home/observer/observer.py --http http://10.78.0.11:30080 --append http://10.78.0.11:30081 --kubeconfig /home/observer/kubeconfig --seconds 3000 --stop-file /home/observer/{name}.stop --output /home/observer/{name}.jsonl >/home/observer/{name}.log 2>&1 </dev/null &');observers.append(i)
 for label,path in [('canary',f/'site-1/serial.log'),('center',f/'center-0/serial.log')]:
  serial=shlex.join(['python3',str(s/'serial-follow.py'),str(path),'--from-end','--stop-file',str(d/'serial.stop'),'--max-seconds','3000'])
  p=subprocess.Popen(['asciinema','rec','--cols','110','--rows','30','-q','-c',serial,str(d/(label+'.cast'))],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL);recorders.append((label,p))
 network(1,'clean');network(2,'lossy');network(3,'blackhole');kube('get','fleetnodes',visible=True)
 version='0.1.0-field-main-init-crash';bundle=root/'www'/(version+'.tar');sha=subprocess.check_output(['sha256sum',str(bundle)],text=True).split()[0]
 rollout={'apiVersion':'fleet.kuberoot.dev/v1alpha1','kind':'FleetRollout','metadata':{'name':'field-failed-canary'},'spec':{'release':{'version':version,'url':'http://10.0.2.2:15004/'+bundle.name,'sha256':sha},'selector':{'matchLabels':{'field-suite':'edge'}},'waves':['1','100%'],'maxFailures':0,'offline':'Wait'}}
 (d/'rollout-request.json').write_text(json.dumps(rollout,indent=2));typed(['date','-u']);subprocess.run(['date','-u']);kube('create','-f',str(d/'rollout-request.json'),visible=True);start=time.monotonic();center_restarted=False;manager_cut=None;old_leader=None;terminal_since=None;max_concurrent=0;last=None;stable_terminal=None
 with (d/'observations.jsonl').open('x') as out:
  while time.monotonic()-start<2100:
   row={'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'elapsed_seconds':time.monotonic()-start}
   try:
    state=snapshot();row.update(state);nodes=state['nodes']['items'];status=state['rollout'].get('status',{});holder=state['lease']['spec'].get('holderIdentity');updates={x['metadata']['name']:x.get('status',{}).get('update',{}) for x in nodes}
    concurrent=sum(x.get('phase') in ['Downloading','Rebooting','Verifying'] for x in updates.values());max_concurrent=max(max_concurrent,concurrent)
    others=[x['metadata']['name'] for x in nodes if x['metadata']['name']!='site-1' and x.get('spec',{}).get('release',{}).get('version')==version]
    if others:metrics['unexpected_second_wave_nodes']=others
    signature=(status.get('phase'),status.get('wave'),holder,tuple((n,u.get('phase')) for n,u in updates.items()))
    if signature!=last:print(json.dumps({'utc':row['utc'],'rollout':status,'leader':holder,'updates':updates}),flush=True);last=signature
    if not center_restarted and row['elapsed_seconds']>=60 and concurrent:
     typed(['date','-u']);subprocess.run(['date','-u']);typed(['python3',str(s/'qmp.py'),str(f/'center-0/qmp.sock'),'quit']);command(str(f/'center-0/qmp.sock'),'quit');time.sleep(2)
     (d/'center-before-reboot.log').write_bytes((f/'center-0/serial.log').read_bytes());subprocess.run(['bash',str(s/'fleet-field-vm.sh'),'center','0'],check=True);center_restarted=True;metrics['center_restart_elapsed_seconds']=row['elapsed_seconds']
    if center_restarted and manager_cut is None and row['elapsed_seconds']>=180:
     old_leader=holder;index=int(holder.rsplit('-',1)[1]);supervisor=int((f/f'manager-{index}-supervisor.pid').read_text());child=int((f/f'manager-{index}.pid').read_text())
     typed(['kill','-STOP',str(supervisor)]);os.kill(supervisor,signal.SIGSTOP);stopped_supervisor=supervisor
     typed(['kill','-9',str(child)]);os.kill(child,signal.SIGKILL);manager_cut=time.monotonic();metrics['killed_leader']=old_leader
    if manager_cut is not None and holder!=old_leader and holder and 'lease_takeover_seconds' not in metrics:metrics['lease_takeover_seconds']=time.monotonic()-manager_cut
    if manager_cut is not None and holder!=old_leader and holder and all(x.get('status',{}).get('phase')=='Connected' for x in nodes if x['metadata']['name'] in ['site-1','site-2']) and 'connected_sites_after_takeover_seconds' not in metrics:metrics['connected_sites_after_takeover_seconds']=time.monotonic()-manager_cut
    terminal=updates.get('site-1',{}).get('phase') in ['RolledBack','Failed'] and status.get('phase')=='Stopped'
    if terminal:
     if terminal_since is None:terminal_since=time.monotonic();stable_terminal=updates.get('site-1');metrics['failed_canary_stopped_seconds']=row['elapsed_seconds']
     if updates.get('site-1',{}).get('since')!=stable_terminal.get('since'):metrics['failed_release_retried']=True
     if time.monotonic()-terminal_since>=300:break
    elif terminal_since is not None:metrics['failed_release_retried']=True
   except Exception as exc:row['error']=str(exc)
   out.write(json.dumps(row)+'\n');out.flush();time.sleep(5)
 metrics['max_concurrent_updates']=max_concurrent
 if not center_restarted:errors.append('center restart during rollout not reached')
 if terminal_since is None:errors.append('failed canary did not produce a stopped rollout within observation budget')
 if metrics.get('unexpected_second_wave_nodes'):errors.append('second wave received failed canary release')
 if max_concurrent>1:errors.append('more than one concurrent update before failed canary stopped')
 if metrics.get('failed_release_retried'):errors.append('failed canary release retried without spec change')
 if metrics.get('connected_sites_after_takeover_seconds',9999)>300:errors.append('connected sites did not recover within 300 seconds after leader kill')
 # The restricted certificate was separately proved able to read inventory.
 active=int(leaseholder().rsplit('-',1)[1]);proxy=f/'restricted-user/proxy-kubeconfig'
 cfg=json.loads(proxy.read_text())
 for c in cfg['clusters']:c['cluster']['server']=f'https://127.0.0.1:{17442+active}/nodes/site-1'
 proxy.write_text(json.dumps(cfg));typed(['export','KUBECONFIG='+str(proxy)]);args=['kubectl','--request-timeout=10s','get','--raw','/apis/node.kuberoot.dev/v1alpha1/bootentries'];typed(args)
 r=subprocess.run(args,env=dict(env,KUBECONFIG=str(proxy)),text=True,capture_output=True,timeout=15);print(r.stdout+r.stderr,flush=True);(d/'restricted-proxy.txt').write_text(r.stdout+r.stderr);metrics['restricted_proxy_denied']=r.returncode!=0 and ('Forbidden' in r.stdout+r.stderr or 'forbidden' in r.stdout+r.stderr)
 if not metrics['restricted_proxy_denied']:errors.append('restricted proxy denial not observed')
 # Revoke the offline site's identity by deleting inventory, then reconnect it.
 kube('delete','fleetnode','site-3',visible=True);network(3,'clean');time.sleep(125)
 returned=subprocess.run(['kubectl','--request-timeout=5s','get','fleetnode','site-3','-o','json'],env=env,text=True,capture_output=True,timeout=8)
 node_config=f/'trust/admin.kubeconfig';node_args=['kubectl','--server=https://127.0.0.1:29513','--request-timeout=5s','get','fleetenrollments','-o','json'];r=subprocess.run(node_args,env=dict(env,KUBECONFIG=str(node_config)),text=True,capture_output=True,timeout=8)
 if r.returncode==0:
  obj=json.loads(r.stdout);public=[{'name':x['metadata']['name'],'status':x.get('status',{})} for x in obj.get('items',[])];(d/'revoked-site-status.json').write_text(json.dumps(public,indent=2));print(json.dumps(public),flush=True)
  metrics['revoked_site_rejected']=returned.returncode!=0 and bool(public) and all(x['status'].get('phase')=='Disconnected' for x in public) and any(any(word in x['status'].get('message','').lower() for word in ['403','401','forbidden','unauthorized','certificate','identity']) for x in public)
 else:metrics['revoked_site_rejected']=False
 if not metrics['revoked_site_rejected']:errors.append('revoked site reconnect rejection not established')
 for i in observers:
  gateway(i,f'touch /home/observer/E10-site-{i}.stop');time.sleep(3)
  (d/f'site-{i}-observer.jsonl').write_text(gateway(i,f'cat /home/observer/E10-site-{i}.jsonl').stdout)
  r=gateway(i,'curl --fail --max-time 20 http://10.78.0.11:30081/records');(d/f'site-{i}-records').write_text(r.stdout)
  check=subprocess.run(['python3',str(s/'check-records.py'),str(d/f'site-{i}-observer.jsonl'),str(d/f'site-{i}-records')],text=True,capture_output=True);metrics[f'site_{i}_durability']=json.loads(check.stdout)
  if check.returncode:errors.append(f'site {i} lost/corrupted acknowledged records')
  samples=[json.loads(x) for x in (d/f'site-{i}-observer.jsonl').read_text().splitlines()]
  metrics[f'site_{i}_local_service_failures']=sum(x['kind'] in ['http','append'] and not x.get('ok') for x in samples)
  if i!=1 and metrics[f'site_{i}_local_service_failures']:errors.append(f'untouched site {i} local W had failed requests')
 typed(['date','-u']);subprocess.run(['date','-u']);kube('get','fleetnodes',visible=True);kube('get','fleetrollouts',visible=True)
except Exception as exc:errors.append(str(exc))
finally:
 for i in observers:gateway(i,f'touch /home/observer/E10-site-{i}.stop')
 if stopped_supervisor:os.kill(stopped_supervisor,signal.SIGCONT)
 (d/'serial.stop').touch()
 for label,p in recorders:
  p.wait(timeout=15);subprocess.run(['python3',str(s/'render-serial.py'),str(d/(label+'.cast'))],check=True)
record={'scenario_id':'E10','product':'kuberoot-main-1b29543','source_commit':os.environ['SOURCE_COMMIT'],'image_digest':os.environ['IMAGE_DIGEST'],'vcpu':2,'ram_mib':3072,'topology':'3 sites at 3 GiB, center datastore at 2 GiB, 3 NAT gateways at 512 MiB, retained shared gateway at 512 MiB; 13 GiB guest total; 2 supervised managers in owned pod; shared 6 CPU limit','fault_seed':None,'fault_phase':'clean/lossy/offline sites; failed canary; center VM restart; kill active manager; restricted proxy; revoked site reconnect','metrics':metrics,'outage_seconds':None,'recovery_seconds':metrics.get('connected_sites_after_takeover_seconds'),'acknowledged_record_loss':sum(m['acknowledged_record_loss'] for k,m in metrics.items() if k.endswith('_durability')),'manual_actions':['restore killed manager supervisor only after measured failover'],'status':'failed' if errors else 'passed','reason':'; '.join(errors) if errors else 'canary, persistence, takeover, proxy and revocation gates passed','time_utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'artifact_paths':[str(d)],'measurement_notes':['Both manager replicas and all VMs share one workshop pod and physical failure domain. This checks semantic process takeover, not host HA.','Guest vCPUs are oversubscribed against the 6 CPU pod quota; per-site RAM reduced from the single-site 4 GiB baseline to fit the fixed limit.']}
(d/'result.json').write_text(json.dumps(record,indent=2));print(json.dumps(record),flush=True)
