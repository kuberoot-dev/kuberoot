#!/usr/bin/env python3
"""Two real 10-minute/2-Mbit windows, one hour apart; never compress real time.
A real signed bundle is used at its measured size, not truncated to 200 MiB.
Requested cut thresholds that cannot be reached are reported as such.
"""
import argparse,datetime,json,os,pathlib,re,shlex,subprocess,time
from qmp import command
p=argparse.ArgumentParser();p.add_argument('--golden',type=pathlib.Path,required=True);p.add_argument('--output',type=pathlib.Path,required=True);p.add_argument('--cut-percent',type=int,choices=[10,50,90],required=True);a=p.parse_args()
root=pathlib.Path('/work/field-edge');s=pathlib.Path(__file__).resolve().parent;d=a.output;assert d.is_relative_to(root);d.mkdir();env=dict(os.environ,VM_DIR=str(d));node_env=dict(env,KUBECONFIG=os.environ['NODE_KUBECONFIG']);G=str(s/'gateway-command.sh');name=d.name;errors=[];metrics={};manual=[];recorder=None
for f in ['system.raw','vars.fd']:subprocess.run(['cp','--sparse=always',str(a.golden/f),str(d/f)],check=True)
def typed(args):
 print('$ ',end='',flush=True)
 for ch in shlex.join([str(x) for x in args]):print(ch,end='',flush=True);time.sleep(.01)
 print(flush=True)
def kube(*args,node=False,visible=False):
 cmd=['kubectl','--request-timeout=5s',*args]
 if visible:typed(cmd)
 r=subprocess.run(cmd,env=node_env if node else env,text=True,capture_output=True,timeout=8)
 if visible:print(r.stdout+r.stderr,end='',flush=True)
 return r
def gateway(cmd):return subprocess.run([G,cmd],text=True,capture_output=True,timeout=30)
def network(mode):
 typed(['date','-u']);subprocess.run(['date','-u']);typed(['bash',str(s/'wan-impairment.sh'),mode]);subprocess.run(['bash',str(s/'wan-impairment.sh'),mode],check=True)
def counters():
 r=gateway('sudo nft -j list table inet field_transfer')
 if r.returncode:raise RuntimeError(r.stderr)
 obj=json.loads(r.stdout);return sum(e.get('counter',{}).get('bytes',0) for rule in obj['nftables'] if 'rule' in rule for e in rule['rule'].get('expr',[]))
def create_request(number):
 request={'apiVersion':'node.kuberoot.dev/v1alpha1','kind':'Upgrade','metadata':{'name':'field-window-'+str(number)},'spec':{'url':os.environ['GOOD_BUNDLE_URL'],'sha256':os.environ['GOOD_BUNDLE_SHA256'],'reboot':True}}
 path=d/f'upgrade-request-{number}.json';path.write_text(json.dumps(request,indent=2))
 r=kube('create','-f',str(path),node=True,visible=True)
 if r.returncode:raise RuntimeError(r.stderr)
 return request['metadata']['name']
try:
 subprocess.run(['bash','-c','source "$1/lib.sh"; vm_start 1','_',str(s)],env=env,check=True)
 for _ in range(150):
  r=kube('get','--raw','/readyz')
  if r.returncode==0 and gateway('curl --silent --max-time 2 http://10.78.0.11:30080/http').stdout=='W'*1024:break
  time.sleep(2)
 else:raise RuntimeError('golden W not ready')
 before=json.loads(kube('get','bootentries','-o','json',node=True).stdout);(d/'bootentries-before.json').write_text(json.dumps(before,indent=2))
 node=json.loads(kube('get','nodes','-o','json').stdout)['items'][0];before_boot=node['status']['nodeInfo']['bootID']
 bundle=root/'www'/os.environ['GOOD_BUNDLE_URL'].rsplit('/',1)[1];metrics['actual_signed_bundle_bytes']=bundle.stat().st_size;metrics['requested_cut_percent']=a.cut_percent;metrics['ideal_bytes_per_10min_window']=150000000;metrics['bundle_differs_from_proposed_200MiB']=bundle.stat().st_size!=200*1024**2
 subprocess.run(['bash',str(s/'prepare-observer.sh')],env=env,check=True)
 gateway('sudo nft delete table inet field_transfer 2>/dev/null || true; sudo nft add table inet field_transfer; sudo nft add chain inet field_transfer forwarded "{ type filter hook forward priority 0; policy accept; }"; sudo nft add rule inet field_transfer forwarded ip daddr 10.78.0.11 tcp sport 15004 counter')
 gateway(f'nohup python3 /home/observer/observer.py --http http://10.78.0.11:30080 --append http://10.78.0.11:30081 --kubeconfig /home/observer/kubeconfig --seconds 4500 --stop-file /home/observer/{name}.stop --output /home/observer/{name}.jsonl >/home/observer/{name}.log 2>&1 </dev/null &')
 serial=shlex.join(['python3',str(s/'serial-follow.py'),str(d/'serial.log'),'--from-end','--stop-file',str(d/'serial.stop'),'--max-seconds','4500'])
 recorder=subprocess.Popen(['asciinema','rec','--cols','110','--rows','30','-q','-c',serial,str(d/'serial.cast')],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
 time.sleep(60);network('slow');start=time.monotonic();start_epoch=time.time();request=create_request(1);window=0;connected=True;cut=False;download_peaks={};last=None;last_phase=None;wire_start=counters();connected_at=start;connected_seconds=0
 typed(['export','KUBECONFIG='+node_env['KUBECONFIG']])
 with (d/'transfer-progress.jsonl').open('x') as out:
  while time.monotonic()-start<4200:
   elapsed=time.monotonic()-start;current_window=int(elapsed//3600);should_connect=elapsed%3600<600
   if current_window!=window:
    window=current_window;network('slow');connected=True;connected_at=time.monotonic()
    if last_phase=='Failed':
     manual.append('Create a new Upgrade in second window after previous download failed; no manual data repair')
     request=create_request(window+1)
   if connected and not should_connect:
    connected_seconds+=time.monotonic()-connected_at;network('blackhole');connected=False
   r=kube('get','upgrade',request,'-o','json',node=True);row={'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'elapsed_seconds':elapsed,'window':window+1,'connected':connected,'request':request}
   if r.returncode==0:
    obj=json.loads(r.stdout);status=obj.get('status',{});row['upgrade_status']=status;last_phase=status.get('phase')
    progress=2*status.get('progress',0) if last_phase=='Downloading' else 100 if last_phase in ['Writing','Staged'] else None
    row['download_percent_quantized']=progress
    if progress is not None:download_peaks[window+1]=max(download_peaks.get(window+1,0),progress)
    if connected and not cut and progress is not None and progress>=a.cut_percent:
     metrics['requested_cut_reached']=True;metrics['cut_at_download_percent_quantized']=progress;metrics['cut_elapsed_seconds']=elapsed
     connected_seconds+=time.monotonic()-connected_at;network('blackhole');connected=False;cut=True
    signature=(window,connected,last_phase,progress)
    if signature!=last:print(json.dumps(row),flush=True);last=signature
   else:row['api_error']=r.stderr
   row['forwarded_download_ipv4_bytes']=counters()-wire_start
   out.write(json.dumps(row)+'\n');out.flush();time.sleep(5)
 if connected:connected_seconds+=time.monotonic()-connected_at
 network('blackhole');metrics['connected_seconds']=connected_seconds;metrics['download_peaks_percent_by_window']=download_peaks;metrics['forwarded_download_ipv4_bytes']=counters()-wire_start
 metrics['requested_cut_reached']=bool(metrics.get('requested_cut_reached'));metrics['final_phase']=last_phase
 after=json.loads(kube('get','bootentries','-o','json',node=True).stdout);(d/'bootentries-after.json').write_text(json.dumps(after,indent=2))
 after_node=json.loads(kube('get','nodes','-o','json').stdout)['items'][0];metrics['boot_id_unchanged']=after_node['status']['nodeInfo']['bootID']==before_boot
 if last_phase not in ['Writing','Staged'] and not metrics['boot_id_unchanged']:errors.append('boot changed before a verified/staged release was observed')
 metrics['observed_restart_in_second_window']=len(manual)>0
 # Preserve real request Range evidence for this interval only.
 server=root/'bundle-requests.jsonl'
 if server.exists():
  rows=[json.loads(x) for x in server.read_text().splitlines()]
  rows=[r for r in rows if datetime.datetime.fromisoformat(r['utc']).timestamp()>=start_epoch and r.get('path','').split('?')[0].endswith(bundle.name)]
  (d/'bundle-requests.jsonl').write_text(''.join(json.dumps(r)+'\n' for r in rows));metrics['range_requests']=sum(bool(r.get('range')) for r in rows)
 gateway(f'touch /home/observer/{name}.stop');time.sleep(4)
 (d/'observer.jsonl').write_text(gateway(f'cat /home/observer/{name}.jsonl').stdout)
 r=gateway('curl --fail --max-time 20 http://10.78.0.11:30081/records');(d/'records').write_text(r.stdout)
 check=subprocess.run(['python3',str(s/'check-records.py'),str(d/'observer.jsonl'),str(d/'records')],text=True,capture_output=True);metrics.update(json.loads(check.stdout))
 if check.returncode:errors.append('acknowledged records lost or corrupt')
 samples=[json.loads(x) for x in (d/'observer.jsonl').read_text().splitlines()];metrics['service_failed']=sum(r['kind'] in ['http','append'] and not r.get('ok') for r in samples)
 if metrics['service_failed']:errors.append('existing local W had failed requests')
 typed(['date','-u']);subprocess.run(['date','-u'])
except Exception as exc:errors.append(str(exc))
finally:
 network('clean');gateway(f'touch /home/observer/{name}.stop')
 try:command(str(d/'qmp.sock'),'quit')
 except Exception:pass
 if recorder:
  (d/'serial.stop').touch();recorder.wait(timeout=15)
  timing=json.loads(subprocess.check_output(['python3','/work/scripts/render.py',str(d/'serial.cast')],text=True))
  subprocess.run(['python3',str(s/'bound-cast.py'),str(d/'serial.retimed.cast')],check=True,stdout=(d/'serial-speed.json').open('w'))
  subprocess.run(['agg','-q','--font-size','16','--theme','monokai','--last-frame-duration',str(max(5,15.1-timing['playback_seconds'])),str(d/'serial.retimed.cast'),str(d/'serial.gif')],check=True)
record={'scenario_id':'E9','product':os.environ['PRODUCT'],'source_commit':os.environ['SOURCE_COMMIT'],'image_digest':os.environ['IMAGE_DIGEST'],'fault_seed':None,'fault_phase':f'cut at requested {a.cut_percent}%; two real 10-minute windows one hour apart','vcpu':2,'ram_mib':4096,'topology':'real signed bundle; 2 Mbit/s guest gateway in both directions; unmodified wall-clock 600/3600 schedule','metrics':metrics,'outage_seconds':None,'recovery_seconds':None,'acknowledged_record_loss':metrics.get('acknowledged_record_loss'),'manual_actions':manual,'status':'failed' if errors else 'measured-limit' if not metrics.get('requested_cut_reached') else 'measured','reason':'; '.join(errors) if errors else 'Actual transfer/restart cost observed; requested cut not reachable under these windows' if not metrics.get('requested_cut_reached') else 'Transfer interruption and next-window behavior measured without shortening offline time','time_utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'artifact_paths':[str(d)],'measurement_notes':['Signed artifact retains its actual size; the proposed 200 MiB cannot be substituted by truncation.','Forwarded bytes are IPv4 packet bytes on the gateway, including transport headers; progress is quantized by the product API.','A new Upgrade after Failed is an explicit operator retry, not automatic resume.']}
(d/'result.json').write_text(json.dumps(record,indent=2));print(json.dumps(record),flush=True)
