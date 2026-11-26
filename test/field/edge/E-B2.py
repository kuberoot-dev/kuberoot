#!/usr/bin/env python3
"""Ten installed cold boots from immutable raw+NVRAM clones.
Prepare GOLDEN after caching the pinned Python image, then stop the source DUT.
No previous VM may use the fixed DUT MAC. Measurements are external wall time.
"""
import argparse,datetime,json,os,pathlib,shlex,signal,subprocess,time
from qmp import command
p=argparse.ArgumentParser();p.add_argument('--golden',type=pathlib.Path,required=True);p.add_argument('--output',type=pathlib.Path,required=True);p.add_argument('--product',required=True);p.add_argument('--image',required=True)
a=p.parse_args();assert '@sha256:' in a.image
scripts=pathlib.Path(__file__).resolve().parent
a.output.mkdir(parents=True,exist_ok=False)
def k(*args,check=True):
 r=subprocess.run(['kubectl','--request-timeout=2s',*args],text=True,capture_output=True,timeout=4)
 if check and r.returncode: raise RuntimeError(r.stderr)
 return r
for repetition in range(1,11):
 d=a.output/str(repetition);d.mkdir()
 for f in ['system.raw','vars.fd']:
  subprocess.run(['cp','--sparse=always',str(a.golden/f),str(d/f)],check=True)
 env=dict(os.environ,VM_DIR=str(d))
 recorder=None
 if repetition==1:
  (d/'serial.log').touch()
  shell='date -u; tail -n +1 -F '+shlex.quote(str(d/'serial.log'))
  recorder=subprocess.Popen(['asciinema','rec','--cols','110','--rows','30','-q','-c',shell,str(d/'serial.cast')],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,start_new_session=True)
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
  r=k('-n','default','exec',name,'--','python3','-c','import socket; print(socket.gethostbyname("kubernetes.default.svc.cluster.local"))')
  metrics['dns_answer']=r.stdout.strip();metrics['coredns_answer_seconds']=time.monotonic()-t0
  k('-n','default','expose','pod',name,'--type=NodePort','--port=8080')
  svc=json.loads(k('-n','default','get','service',name,'-o','json').stdout)
  port=svc['spec']['ports'][0]['nodePort']
  r=subprocess.run([str(scripts/'gateway-command.sh'),f'curl --fail --max-time 2 http://10.78.0.11:{port}/'],text=True,capture_output=True,timeout=10)
  if r.returncode or 'Directory listing' not in r.stdout:raise RuntimeError('new pod HTTP response missing/wrong')
  metrics['new_pod_serves_seconds']=time.monotonic()-t0
 except Exception as exc:errors.append(str(exc))
 finally:
  try: command(str(d/'qmp.sock'),'quit')
  except Exception as exc: errors.append('QMP stop: '+str(exc))
  if recorder:
   os.killpg(recorder.pid,signal.SIGINT)
   recorder.wait(timeout=10)
   subprocess.run(['python3','/work/scripts/render.py',str(d/'serial.cast')],check=True)
   subprocess.run(['/work/bin/agg','--font-size','16','--theme','monokai','--last-frame-duration','5',str(d/'serial.retimed.cast'),str(d/'serial.gif')],check=True)
 result={'scenario_id':'E-B2','product':a.product,'repetition':repetition,'start_utc':utc0.isoformat(),'metrics':metrics,'errors':errors,'status':'measured' if not errors else 'failed','vcpu':2,'ram_mib':4096,'topology':'same multicast LAN and gateway; fresh raw disk and OVMF vars clone','workload_image':a.image,'artifact_paths':[str(d/'serial.log')]}
 (d/'result.json').write_text(json.dumps(result,indent=2));print(json.dumps(result),flush=True)
 # Successful disks can be reused/recreated; keep failed disks for diagnosis.
 if not errors:(d/'system.raw').unlink()
 time.sleep(2)
