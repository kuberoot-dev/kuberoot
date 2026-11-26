#!/usr/bin/env python3
"""Hidden dead-node/API-path faults followed by a real blank-disk replacement.
Management hostfwd removal is explicitly an API-path failure, not a stopped API
server process. Replacement hooks must return measured public evidence only.
"""
import argparse,datetime,json,os,pathlib,shlex,subprocess,time,urllib.request
from qmp import command
p=argparse.ArgumentParser();p.add_argument('--ha-root',type=pathlib.Path,required=True);p.add_argument('--product',required=True);p.add_argument('--replace-hook',type=pathlib.Path,required=True);a=p.parse_args();root=pathlib.Path('/work/field-edge');s=pathlib.Path(__file__).resolve().parent;h=a.ha_root;assert h.is_relative_to(root);d=h/'E7';d.mkdir();env=dict(os.environ,HA_ROOT=str(h),KUBECONFIG=str(h/'kubeconfig-1'),CASE_OUTPUT=str(d));errors=[];metrics={};observer=None;recorder=None;vga=None;fault_injected=False

def typed(args):
 print('$ ',end='',flush=True)
 for ch in shlex.join([str(x) for x in args]):print(ch,end='',flush=True);time.sleep(.01)
 print(flush=True)
def kube(*args,index=1):return subprocess.run(['kubectl','--request-timeout=5s',*args],env=dict(env,KUBECONFIG=str(h/f'kubeconfig-{index}')),text=True,capture_output=True,timeout=8)
def node3():
 obj=json.loads(kube('get','nodes','-o','json').stdout)
 return next(n for n in obj['items'] if any(ip['address']=='10.79.0.13' for ip in n['status']['addresses']))
def ready(node):return any(c['type']=='Ready' and c['status']=='True' for c in node['status']['conditions'])
def api_access(enabled):
 line=('hostfwd_add management tcp:127.0.0.1:26343-:6443' if enabled else 'hostfwd_remove management tcp:127.0.0.1:26343')
 typed(['python3',str(s/'qmp.py'),str(h/'n3/qmp.sock'),'human-monitor-command',json.dumps({'command-line':line})]);reply=command(str(h/'n3/qmp.sock'),'human-monitor-command',{'command-line':line});print(reply,flush=True)
def stamp():return datetime.datetime.now(datetime.timezone.utc).isoformat()
try:
 before=node3();oldname=before['metadata']['name'];oldboot=before['status']['nodeInfo']['bootID'];env['OLD_NODE_NAME']=oldname
 (d/'node-before.json').write_text(json.dumps(before,indent=2));typed(['export','KUBECONFIG='+env['KUBECONFIG']]);typed(['kubectl','get','nodes','-o','wide']);print(kube('get','nodes','-o','wide').stdout,flush=True)
 observer_config=d/'observer-kubeconfig.private.json'
 observer_config.write_text(subprocess.check_output(['kubectl','config','view','--raw','--minify','-o','json'],env=env,text=True));observer_config.chmod(0o600)
 observer=subprocess.Popen(['python3',str(s/'observer.py'),'--http','http://127.0.0.1:28081','--append','http://127.0.0.1:28091','--kubeconfig',str(observer_config),'--seconds','1800','--stop-file',str(d/'observer.stop'),'--output',str(d/'observer.jsonl')],stdout=(d/'observer.log').open('w'),stderr=subprocess.STDOUT)
 serial=shlex.join(['python3',str(s/'serial-follow.py'),str(h/'n3/serial.log'),'--from-end','--stop-file',str(d/'serial.stop'),'--max-seconds','1800'])
 recorder=subprocess.Popen(['asciinema','rec','--cols','110','--rows','30','-q','-c',serial,str(d/'serial.cast')],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
 if a.product.startswith('talos'):
  vga=subprocess.Popen(['python3',str(s/'vga-capture.py'),'--qmp',str(h/'n3/qmp.sock'),'--output',str(d/'vga'),'--stop-file',str(d/'serial.stop'),'--seconds','1800','--interval','3'],stdout=(d/'vga-capture.log').open('w'),stderr=subprocess.STDOUT)
 time.sleep(30)
 if observer.poll() is not None:raise RuntimeError('external observer exited before E7 injection; inspect private observer log')
 initial=[json.loads(line) for line in (d/'observer.jsonl').read_text().splitlines() if line.endswith('}')]
 for kind in ['http','append','api']:
  if not any(r['kind']==kind and r.get('ok') for r in initial):raise RuntimeError('external '+kind+' baseline failed before E7 injection')
 typed(['date','-u']);subprocess.run(['date','-u']);typed(['python3',str(s/'qmp.py'),str(h/'n3/qmp.sock'),'quit']);command(str(h/'n3/qmp.sock'),'quit');fault_injected=True;fault=time.monotonic();service_success=0
 with (d/'node-detection.jsonl').open('x') as out:
  while time.monotonic()-fault<125:
   node=node3();http=False
   try:http=urllib.request.urlopen('http://127.0.0.1:28081/http',timeout=2).read()==b'W'*1024
   except Exception:pass
   service_success+=http;row={'utc':stamp(),'seconds_since_node_stop':time.monotonic()-fault,'node_conditions':node['status']['conditions'],'service_ok':http};out.write(json.dumps(row)+'\n');out.flush();print(json.dumps(row),flush=True)
   if not ready(node) and 'dead_node_detected_seconds' not in metrics:metrics['dead_node_detected_seconds']=row['seconds_since_node_stop']
   if time.monotonic()-fault>70 and 'dead_node_detected_seconds' in metrics:break
   time.sleep(5)
 metrics['healthy_service_responses_while_node_dead']=service_success
 if metrics.get('dead_node_detected_seconds',9999)>120:errors.append('dead node not visible within 120 seconds')
 if not service_success:errors.append('healthy service was not demonstrated while one node was dead')
 subprocess.run(['bash',str(s/'ha-vm.sh'),'start','3'],env=env,check=True)
 for _ in range(120):
  node=node3()
  if ready(node) and node['status']['nodeInfo']['bootID']!=oldboot:break
  time.sleep(3)
 else:raise RuntimeError('node did not recover before separate API-path fault')
 api_access(False);fault=time.monotonic();r=kube('get','--raw','/readyz',index=3);metrics['kubernetes_api_path_failed']=r.returncode!=0;(d/'api-path-failure.txt').write_text(r.stdout+r.stderr)
 if a.product.startswith('kuberoot'):
  e=dict(env,KUBECONFIG=str(h/'trust/admin.kubeconfig'));cmd=['kubectl','--server=https://127.0.0.1:25300','--tls-server-name='+oldname+'.kuberoot-node.kube-system.svc','--request-timeout=5s','get','osconfigs'];typed(['export','KUBECONFIG='+e['KUBECONFIG']]);typed(cmd);node_api=subprocess.run(cmd,env=e,text=True,capture_output=True,timeout=8)
 elif a.product.startswith('talos'):
  cmd=['talosctl','--talosconfig',str(h/'config/talosconfig'),'--endpoints','127.0.0.1:25300','--nodes','10.79.0.13','version'];typed(cmd);node_api=subprocess.run(cmd,text=True,capture_output=True,timeout=15)
 else:node_api=None;metrics['separate_node_api']='unsupported: k3s has no independent host-management API in this product lane'
 if node_api:
  print(node_api.stdout+node_api.stderr,flush=True);(d/'node-api-during-fault.txt').write_text(node_api.stdout+node_api.stderr);metrics['node_api_still_available']=node_api.returncode==0
  if node_api.returncode:errors.append('node API was not available during Kubernetes API-path fault')
 metrics['api_path_fault_detected_seconds']=time.monotonic()-fault
 if not metrics['kubernetes_api_path_failed']:errors.append('Kubernetes API-path fault was not established')
 time.sleep(30);api_access(True)
 # The hook quarantines the stopped old disk, uses a different NIC MAC, installs
 # on a blank disk, enrolls via product APIs/cloud-init, and records every action.
 typed(['bash',str(a.replace_hook)]);subprocess.run(['bash',str(a.replace_hook)],env=env,check=True)
 replacement=json.loads((d/'replacement.json').read_text());metrics.update(replacement)
 if replacement.get('replacement_ready_seconds',9999)>600:errors.append('blank replacement did not join within 10 minutes')
 if not replacement.get('old_identity_revoked'):errors.append('old enrollment identity/credential revocation not established')
 if not replacement.get('no_duplicate_member'):errors.append('absence of duplicate datastore member not established')
 (d/'observer.stop').touch();observer.wait(timeout=15)
 (d/'records').write_bytes(urllib.request.urlopen('http://127.0.0.1:28091/records',timeout=20).read())
 check=subprocess.run(['python3',str(s/'check-records.py'),str(d/'observer.jsonl'),str(d/'records')],text=True,capture_output=True);metrics.update(json.loads(check.stdout))
 if check.returncode:errors.append('acknowledged application records lost or corrupt')
 typed(['date','-u']);subprocess.run(['date','-u']);typed(['export','KUBECONFIG='+env['KUBECONFIG']]);typed(['kubectl','get','nodes','-o','wide']);print(kube('get','nodes','-o','wide').stdout,flush=True)
except Exception as exc:errors.append(str(exc))
finally:
 (d/'observer.stop').touch()
 if observer:
  try:observer.wait(timeout=15)
  except subprocess.TimeoutExpired:observer.terminate();observer.wait(timeout=5)
 try:api_access(True)
 except Exception:pass
 if recorder:
  (d/'serial.stop').touch();recorder.wait(timeout=15);subprocess.run(['python3',str(s/'render-serial.py'),str(d/'serial.cast')],check=True)
 if vga:vga.wait(timeout=60)
record={'scenario_id':'E7','product':a.product,'source_commit':os.environ['SOURCE_COMMIT'],'image_digest':os.environ['IMAGE_DIGEST'],'vcpu':2,'ram_mib':4096,'node_count':3,'topology':'3 control planes; shared multicast peer LAN; separate user-net API observation; W via healthy node 1','fault_seed':None,'fault_phase':'stop node 3; block its observed Kube API path while host API remains; blank replacement with new MAC','metrics':metrics,'outage_seconds':None,'recovery_seconds':metrics.get('replacement_ready_seconds'),'acknowledged_record_loss':metrics.get('acknowledged_record_loss'),'manual_actions':metrics.get('operator_actions',[]),'status':'invalid-fixture' if not fault_injected else 'failed' if errors else 'passed','reason':'; '.join(errors) if errors else 'fault detection, replacement, revocation and membership gates passed','time_utc':stamp(),'artifact_paths':[str(d)],'measurement_notes':['API-path fault removes only the QEMU management port forward. The API server process itself is not stopped.','Application append storage remains on surviving node 1; this is not a claim of replicated-PVC failover.']}
(d/'result.json').write_text(json.dumps(record,indent=2));print(json.dumps(record),flush=True)
