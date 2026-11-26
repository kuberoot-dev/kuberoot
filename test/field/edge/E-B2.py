#!/usr/bin/env python3
"""Ten installed cold boots from immutable raw+NVRAM clones.
Prepare GOLDEN after caching the pinned Python image, then stop the source DUT.
No previous VM may use the fixed DUT MAC. Measurements are external wall time.
"""
import argparse,datetime,json,os,pathlib,shlex,signal,subprocess,time
from qmp import command
p=argparse.ArgumentParser();p.add_argument('--golden',type=pathlib.Path,required=True);p.add_argument('--output',type=pathlib.Path,required=True);p.add_argument('--product',required=True);p.add_argument('--image',required=True);p.add_argument('--image-digest',required=True);p.add_argument('--source-commit',required=True)
p.add_argument('--repetition',type=int,choices=range(1,11)); a=p.parse_args();assert '@sha256:' in a.image
scripts=pathlib.Path(__file__).resolve().parent
a.output.mkdir(parents=True,exist_ok=True)
def k(*args,check=True,request_timeout=2):
 try:
  r=subprocess.run(['kubectl',f'--request-timeout={request_timeout}s',*args],text=True,capture_output=True,timeout=request_timeout+3)
 except subprocess.TimeoutExpired as exc:
  if check: raise
  r=subprocess.CompletedProcess(exc.cmd,124,'',str(exc))
 if check and r.returncode: raise RuntimeError(r.stderr)
 return r
for repetition in ([a.repetition] if a.repetition else range(1,11)):
 d=a.output/str(repetition);d.mkdir()
 for f in ['system.raw','vars.fd']:
  subprocess.run(['cp','--sparse=always',str(a.golden/f),str(d/f)],check=True)
 env=dict(os.environ,VM_DIR=str(d))
 recorder=None;vga=None
 if repetition==1:
  (d/'serial.log').touch()
  shell=shlex.join(['python3',str(scripts/'serial-follow.py'),str(d/'serial.log'),'--stop-file',str(d/'serial.stop')])
  recorder=subprocess.Popen(['asciinema','rec','--cols','110','--rows','30','-q','-c',shell,str(d/'serial.cast')],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,start_new_session=True)
 if repetition==1 and a.product.startswith('talos'):
  vga=subprocess.Popen(['python3',str(scripts/'vga-capture.py'),'--qmp',str(d/'qmp.sock'),'--output',str(d/'vga'),'--stop-file',str(d/'serial.stop')],stdout=(d/'vga-capture.log').open('w'),stderr=subprocess.STDOUT)
 t0=time.monotonic();utc0=datetime.datetime.now(datetime.timezone.utc)
 subprocess.run(['bash','-c','source "$1/lib.sh"; vm_start 1','_',str(scripts)],env=env,check=True)
 metrics={};errors=[]
 try:
  deadline=t0+300
  while time.monotonic()<deadline:
   r=k('get','--raw','/readyz',check=False)
   if r.returncode==0 and r.stdout.strip()=='ok':metrics['api_ready_seconds']=time.monotonic()-t0;break
   time.sleep(1)
  else: raise RuntimeError('API did not become ready in 300 seconds')
  while time.monotonic()<deadline:
   r=k('get','nodes','-o','json',check=False)
   if r.returncode==0:
    nodes=json.loads(r.stdout)['items']
    fresh=[n for n in nodes if any(c['type']=='Ready' and c['status']=='True' and datetime.datetime.fromisoformat(c['lastHeartbeatTime'].replace('Z','+00:00'))>=utc0 for c in n['status']['conditions'])]
    if fresh:
     metrics['node_ready_fresh_heartbeat_seconds']=time.monotonic()-t0;metrics['boot_id']=fresh[0]['status']['nodeInfo']['bootID'];break
   time.sleep(1)
  else: raise RuntimeError('fresh Ready heartbeat missing after 300 seconds')
  name=f'boot-probe-{repetition}'
  code='import http.server; http.server.HTTPServer(("0.0.0.0",8080),http.server.SimpleHTTPRequestHandler).serve_forever()'
  k('-n','default','run',name,'--image='+a.image,'--restart=Never','--command','--','python3','-c',code)
  # Waiting client-side, with bounded subprocess timeout separate from kubectl.
  while time.monotonic()<deadline:
   r=k('-n','default','get','pod',name,'-o','json',check=False)
   if r.returncode==0 and any(c['type']=='Ready' and c['status']=='True' for c in json.loads(r.stdout).get('status',{}).get('conditions',[])):break
   time.sleep(1)
  else: raise RuntimeError('new pod not Ready within 300 seconds')
  attempts=[]
  while time.monotonic()<deadline:
   r=k('-n','default','exec',name,'--','python3','-c','import socket; socket.setdefaulttimeout(3); print(socket.gethostbyname("kubernetes.default.svc.cluster.local"))',check=False,request_timeout=10)
   attempts.append({'elapsed_seconds':time.monotonic()-t0,'returncode':r.returncode,'stdout':r.stdout,'stderr':r.stderr})
   (d/'dns-attempts.json').write_text(json.dumps(attempts,indent=2))
   if r.returncode==0 and r.stdout.strip():break
   time.sleep(1)
  else: raise RuntimeError('CoreDNS did not answer within 300 seconds: '+r.stderr)
  metrics['dns_answer']=r.stdout.strip();metrics['coredns_answer_seconds']=time.monotonic()-t0
  k('-n','default','expose','pod',name,'--type=NodePort','--port=8080',request_timeout=10)
  svc=json.loads(k('-n','default','get','service',name,'-o','json',request_timeout=10).stdout)
  port=svc['spec']['ports'][0]['nodePort']
  while time.monotonic()<deadline:
   r=subprocess.run([str(scripts/'gateway-command.sh'),f'curl --fail --max-time 2 http://10.78.0.11:{port}/'],text=True,capture_output=True,timeout=10)
   if r.returncode==0 and 'Directory listing' in r.stdout:break
   time.sleep(1)
  else: raise RuntimeError('new pod HTTP response missing/wrong within 300 seconds')
  metrics['new_pod_serves_seconds']=time.monotonic()-t0
 except Exception as exc:errors.append(str(exc))
 finally:
  try: command(str(d/'qmp.sock'),'quit')
  except Exception as exc: errors.append('QMP stop: '+str(exc))
  if recorder:
   (d/'serial.stop').touch()
   recorder.wait(timeout=10)
   if vga:vga.wait(timeout=60)
   timing=json.loads(subprocess.check_output(['python3','/work/scripts/render.py',str(d/'serial.cast')],text=True))
   subprocess.run(['python3',str(scripts/'bound-cast.py'),str(d/'serial.retimed.cast')],check=True,stdout=(d/'serial-speed.json').open('w'))
   hold=str(max(5,15.1-timing['playback_seconds']))
   subprocess.run(['/work/bin/agg','--font-size','16','--theme','monokai','--last-frame-duration',hold,str(d/'serial.retimed.cast'),str(d/'serial.gif')],check=True)
 result={'measurement_protocol':'boot-readiness-v2','scenario_id':'E-B2','product':a.product,'image_digest':a.image_digest,'source_commit':a.source_commit,'fault_seed':None,'fault_phase':'installed cold boot','outage_seconds':None,'acknowledged_record_loss':None,'recovery_seconds':metrics.get('new_pod_serves_seconds'),'manual_actions':[],'time_utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'reason':'all four boot milestones measured' if not errors else '; '.join(errors),'repetition':repetition,'start_utc':utc0.isoformat(),'metrics':metrics,'errors':errors,'status':'measured' if not errors else 'failed','vcpu':2,'ram_mib':4096,'topology':'same multicast LAN and gateway; fresh raw disk and OVMF vars clone','workload_image':a.image,'artifact_paths':[str(d/'serial.log')]}
 (d/'result.json').write_text(json.dumps(result,indent=2));print(json.dumps({key:result[key] for key in ["product","repetition","status","metrics","errors"]}),flush=True)
 with (a.output/'runs.jsonl').open('a') as f:f.write(json.dumps(result)+'\n')
 # Successful disks can be reused/recreated; keep failed disks for diagnosis.
 if not errors:(d/'system.raw').unlink()
 time.sleep(2)
