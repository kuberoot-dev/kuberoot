#!/usr/bin/env python3
"""Real two-window Talos transfer, native largest-layer cut percentages."""
import argparse,datetime,json,os,pathlib,re,shlex,subprocess,time
from qmp import command
import talos_trial as t
p=argparse.ArgumentParser();p.add_argument('--cut-percent',type=int,choices=[10,50,90],required=True);a=p.parse_args();root=t.ROOT;s=t.S;golden=root/'talos';d=root/f'results/E9-talos-v1.14.7-{a.cut_percent}';d.mkdir();env=dict(os.environ,VM_DIR=str(d));G=str(s/'gateway-command.sh');metrics={};errors=[];manual=[];recorder=process=None;name=d.name;started=False
for f in ['system.raw','vars.fd']:subprocess.run(['cp','--sparse=always',str(golden/f),str(d/f)],check=True)
def typed(args):print('$ '+shlex.join([str(x) for x in args]),flush=True)
def gateway(cmd):return subprocess.run([G,cmd],text=True,capture_output=True,timeout=30)
def network(mode):
 typed(['date','-u']);subprocess.run(['date','-u']);typed(['bash',s/'wan-impairment.sh',mode]);subprocess.run(['bash',str(s/'wan-impairment.sh'),mode],check=True)
def kube(*args):return subprocess.run(['kubectl','--request-timeout=5s',*args],env=env,text=True,capture_output=True,timeout=8)
try:
 subprocess.run(['bash',str(s/'boot-node.sh'),'1'],env=env,check=True)
 for _ in range(150):
  if kube('get','--raw','/readyz').returncode==0 and gateway('curl --silent --max-time 2 http://10.78.0.11:30080/http').stdout=='W'*1024:break
  time.sleep(2)
 else:raise RuntimeError('Talos W not ready')
 t.configure(d);before=t.boot_id();before_entry=t.boot_default();image=json.loads((root/'talos-good/image.json').read_text());largest=max(x['size'] for x in image['layers'])
 metrics.update({'image_reference':image['reference'],'actual_compressed_layer_bytes':image['compressed_layer_bytes'],'largest_compressed_layer_bytes':largest,'requested_cut_percent':a.cut_percent,'cut_denominator':'native progress of largest compressed OCI layer; not whole image or transport bytes','ideal_bytes_per_10min_window':150000000})
 subprocess.run(['bash',str(s/'prepare-observer.sh')],env=env,check=True);t.counter(reset=True)
 gateway(f'nohup python3 /home/observer/observer.py --http http://10.78.0.11:30080 --append http://10.78.0.11:30081 --kubeconfig /home/observer/kubeconfig --seconds 4500 --stop-file /home/observer/{name}.stop --output /home/observer/{name}.jsonl >/home/observer/{name}.log 2>&1 </dev/null &')
 serial=shlex.join(['python3',str(s/'serial-follow.py'),str(d/'serial.log'),'--from-end','--stop-file',str(d/'serial.stop'),'--max-seconds','4500'])
 recorder=subprocess.Popen(['asciinema','rec','--cols','110','--rows','30','-q','-c',serial,str(d/'serial.cast')],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
 time.sleep(60);network('slow');start=time.monotonic();start_epoch=time.time();started=True;process=t.upgrade(d,no_reboot=False);window=0;connected=True;connected_at=time.monotonic();connected_seconds=0;cut=False;native_percent=None;last=None;unverified_boot=False;verified_seen=False
 with (d/'transfer-progress.jsonl').open('x') as out:
  while time.monotonic()-start<4200:
   elapsed=time.monotonic()-start;current=int(elapsed//3600)
   if current!=window:
    window=current;network('slow');connected=True;connected_at=time.monotonic()
    if process.poll() is not None and process.returncode!=0:
     manual.append('Retry native talosctl upgrade in second window after prior command failed');process=t.upgrade(d,no_reboot=False)
   if connected and elapsed%3600>=600:connected_seconds+=time.monotonic()-connected_at;network('blackhole');connected=False
   raw=(d/'talos-upgrade-progress.txt').read_text(errors='replace');plain=re.sub(r'\x1b\[[0-?]*[ -/]*[@-~]','',raw)
   # The pinned CLI formats each layer with rounded total and %. Match only
   # the largest actual manifest layer within the display's rounding precision.
   matches=re.findall(r'downloading layer [0-9.]+\s*[KMGT]?i?B/([0-9.]+)\s*([KMGT]?i?B)\s*\(([0-9.]+)%\)',plain)
   unit={'B':1,'KiB':1024,'MiB':1024**2,'GiB':1024**3,'TiB':1024**4}
   for total,u,pct in matches:
    if u in unit and abs(float(total)*unit[u]-largest)<=max(1024,largest*.015):native_percent=float(pct)
   verified_seen=verified_seen or 'upgrade completed' in plain
   if connected and not cut and native_percent is not None and native_percent>=a.cut_percent:
    metrics.update({'requested_cut_reached':True,'native_layer_percent_at_cut':native_percent,'cut_elapsed_seconds':elapsed});connected_seconds+=time.monotonic()-connected_at;network('blackhole');connected=False;cut=True
   row={'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'elapsed_seconds':elapsed,'window':window+1,'connected':connected,'download_percent_quantized':native_percent,'forwarded_download_ipv4_bytes':t.counter(),'native_upgrade_exit':process.poll(),'verified_install_observed':verified_seen}
   try:
    row['boot_id']=t.boot_id()
    if row['boot_id']!=before and not verified_seen:unverified_boot=True
   except (RuntimeError,subprocess.TimeoutExpired) as exc:row['machine_api_error']=str(exc)
   state=(window,connected,native_percent,process.poll(),verified_seen)
   if state!=last:print(json.dumps(row),flush=True);last=state
   out.write(json.dumps(row)+'\n');out.flush();time.sleep(5)
 if connected:connected_seconds+=time.monotonic()-connected_at
 network('blackhole');metrics.update({'connected_seconds':connected_seconds,'forwarded_download_ipv4_bytes':t.counter(),'requested_cut_reached':bool(metrics.get('requested_cut_reached')),'native_layer_percent_final':native_percent,'verified_install_observed':verified_seen,'unverified_boot_observed':unverified_boot,'native_upgrade_exit':process.poll()})
 if unverified_boot:errors.append('new boot observed before native verified installation completed')
 gateway(f'touch /home/observer/{name}.stop');time.sleep(4);(d/'observer.jsonl').write_text(gateway(f'cat /home/observer/{name}.jsonl').stdout)
 r=gateway('curl --fail --max-time 20 http://10.78.0.11:30081/records');assert r.returncode==0,r.stderr;(d/'records').write_text(r.stdout)
 check=subprocess.run(['python3',str(s/'check-records.py'),str(d/'observer.jsonl'),str(d/'records')],text=True,capture_output=True);metrics.update(json.loads(check.stdout))
 if check.returncode:errors.append('acknowledged records lost or corrupt')
 rows=[json.loads(x) for x in (d/'observer.jsonl').read_text().splitlines()];metrics['service_failed']=sum(r['kind'] in ['http','append'] and not r.get('ok') for r in rows)
 if metrics['service_failed']:errors.append('existing W had failed requests')
 typed(['date','-u']);subprocess.run(['date','-u'])
except Exception as exc:errors.append(str(exc))
finally:
 network('clean');gateway(f'touch /home/observer/{name}.stop')
 if process and process.poll() is None:process.terminate();process.wait(timeout=15)
 try:command(str(d/'qmp.sock'),'quit')
 except Exception:pass
 if recorder:
  (d/'serial.stop').touch();recorder.wait(timeout=15);subprocess.run(['python3',str(s/'render-serial.py'),str(d/'serial.cast')],check=True)
record={'scenario_id':'E9','product':'talos-v1.14.7','source_commit':os.environ['SOURCE_COMMIT'],'image_digest':os.environ['IMAGE_DIGEST'],'vcpu':2,'ram_mib':4096,'fault_seed':None,'fault_phase':f'cut at requested {a.cut_percent}% native largest-layer progress; two actual 10-minute windows one hour apart','topology':'same multicast LAN and 2 Mbit/s gateway; official unchanged installer mirrored by digest','metrics':metrics,'acknowledged_record_loss':metrics.get('acknowledged_record_loss'),'outage_seconds':None,'recovery_seconds':None,'manual_actions':manual,'status':'failed' if errors else 'measured-limit' if not metrics.get('requested_cut_reached') else 'measured','reason':'; '.join(errors) if errors else 'native transfer and restart behavior measured; actual OCI layer size and cut denominator recorded','measurement_notes':['Native per-layer percentages are rounded by talosctl; largest-layer progress is explicitly not whole-image progress.','Forwarded gateway bytes include IPv4/TCP/TLS overhead; they are not treated as payload completion.','Actual artifact size is retained; no truncated or padded 200 MiB fake installer.','A restarted talosctl process is an operator retry, not claimed automatic resume.'],'artifact_paths':[str(d)],'time_utc':datetime.datetime.now(datetime.timezone.utc).isoformat()}
(d/'result.json').write_text(json.dumps(record,indent=2));print(json.dumps(record),flush=True)
