#!/usr/bin/env python3
"""One RTC fault, strict TLS, actual W observation, then a documented correction."""
import argparse,datetime,email.utils,json,os,pathlib,shlex,subprocess,time
from qmp import command
p=argparse.ArgumentParser();p.add_argument('--golden',type=pathlib.Path,required=True);p.add_argument('--output',type=pathlib.Path,required=True);p.add_argument('--product',required=True);p.add_argument('--case',choices=['minus-24h','plus-400d','beyond-ca'],required=True);a=p.parse_args()
scripts=pathlib.Path(__file__).resolve().parent;root=pathlib.Path('/work/field-edge');d=a.output
assert d.is_relative_to(root) and a.golden.is_relative_to(root);d.mkdir(parents=True)
config=json.loads(subprocess.check_output(['kubectl','config','view','--minify','-o','json']))
assert all(c['cluster']['server'].startswith('https://') and not c['cluster'].get('insecure-skip-tls-verify') for c in config['clusters'])
ca=json.loads(subprocess.check_output(['python3',str(scripts/'ca-expiry.py')],text=True));(d/'ca-expiry.json').write_text(json.dumps(ca,indent=2))
now=datetime.datetime.now(datetime.timezone.utc)
rtc={'minus-24h':now-datetime.timedelta(days=1),'plus-400d':now+datetime.timedelta(days=400),'beyond-ca':datetime.datetime.fromisoformat(ca['beyond_ca_rtc'])}[a.case]
for file in ['system.raw','vars.fd']:subprocess.run(['cp','--sparse=always',str(a.golden/file),str(d/file)],check=True)
env=dict(os.environ,VM_DIR=str(d),RTC_BASE=rtc.strftime('%Y-%m-%dT%H:%M:%S'))
name=f'E6-{a.product}-{a.case}';G=str(scripts/'gateway-command.sh');errors=[];metrics={};rows=[];manual=[];recorder=None;vga=None;observer=False;corrected=None

def gateway(text,check=True):return subprocess.run([G,text],text=True,capture_output=True,check=check,timeout=30)
def typed(args):
 print('$ ',end='',flush=True)
 for ch in shlex.join([str(x) for x in args]):print(ch,end='',flush=True);time.sleep(.012)
 print(flush=True)
def probe(phase):
 try:r=subprocess.run(['kubectl','--request-timeout=3s','get','--raw','/readyz'],capture_output=True,text=True,timeout=5)
 except subprocess.TimeoutExpired as exc:r=subprocess.CompletedProcess(exc.cmd,124,'',str(exc))
 row={'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'phase':phase,'returncode':r.returncode,'stdout':r.stdout,'stderr':r.stderr};rows.append(row)
 with (d/'api-diagnosis.jsonl').open('a') as f:f.write(json.dumps(row)+'\n')
 print(json.dumps(row),flush=True);return r.returncode==0 and r.stdout.strip()=='ok'
def pull():
 text=gateway(f'cat /home/observer/{name}.jsonl').stdout
 if text and not text.endswith('\n'):text=text.rsplit('\n',1)[0]+'\n'
 (d/'observer.jsonl').write_text(text)
try:
 subprocess.run(['bash',str(scripts/'prepare-observer.sh'),'--config-only'],check=True,env=env)
 subprocess.run([str(scripts/'wan-impairment.sh'),'blackhole'],check=True)
 gateway(f'nohup python3 /home/observer/observer.py --http http://10.78.0.11:30080 --append http://10.78.0.11:30081 --kubeconfig /home/observer/kubeconfig --seconds 900 --stop-file /home/observer/{name}.stop --output /home/observer/{name}.jsonl >/home/observer/{name}.log 2>&1 </dev/null &');observer=True
 (d/'serial.log').touch()
 cmd=shlex.join(['python3',str(scripts/'serial-follow.py'),str(d/'serial.log'),'--stop-file',str(d/'serial.stop'),'--max-seconds','900'])
 recorder=subprocess.Popen(['asciinema','rec','--cols','110','--rows','30','-q','-c',cmd,str(d/'serial.cast')],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
 if a.product.startswith('talos'):
  vga=subprocess.Popen(['python3',str(scripts/'vga-capture.py'),'--qmp',str(d/'qmp.sock'),'--output',str(d/'vga'),'--seconds','900','--stop-file',str(d/'serial.stop')],stdout=(d/'vga-capture.log').open('w'),stderr=subprocess.STDOUT)
 typed(['export','RTC_BASE='+env['RTC_BASE']]);typed(['bash',str(scripts/'boot-node.sh'),'1'])
 subprocess.run(['bash',str(scripts/'boot-node.sh'),'1'],env=env,check=True)
 wrong_started=time.time();deadline=time.monotonic()+180
 while time.monotonic()<deadline:probe('wrong-RTC');time.sleep(10)
 if a.product.startswith('k3s'):
  clock=subprocess.run([str(scripts/'dut-command.sh'),'date -u +%FT%TZ'],capture_output=True,text=True,timeout=15)
  (d/'guest-clock.txt').write_text(clock.stdout+clock.stderr)
 pull();(d/'wrong-rtc-serial.log').write_bytes((d/'serial.log').read_bytes())
 typed(['python3',str(scripts/'qmp.py'),str(d/'qmp.sock'),'quit']);command(str(d/'qmp.sock'),'quit');time.sleep(2)
 env['RTC_BASE']='utc';typed(['export','RTC_BASE=utc']);typed(['date','-u']);subprocess.run(['date','-u'],check=True)
 manual.append('QMP power-off and cold boot with RTC=utc; no TLS bypass');corrected=time.time()
 subprocess.run(['bash',str(scripts/'boot-node.sh'),'1'],env=env,check=True)
 deadline=time.monotonic()+365;api_ready=None;clock_step=False
 while time.monotonic()<deadline:
  if probe('correct-RTC') and api_ready is None:api_ready=time.time()-corrected
  pull()
  observed=json.loads(subprocess.check_output(['python3',str(scripts/'analyse-observer.py'),str(d/'observer.jsonl'),'--fault-epoch',str(corrected)],text=True));metrics.update(observed)
  auth_ok=any(row.get('kind')=='api' and row.get('ok') and datetime.datetime.fromisoformat(row['utc']).timestamp()>=corrected for line in (d/'observer.jsonl').read_text().splitlines() if (row:=json.loads(line)))
  if observed['recovery_seconds_first_60s_sustained_success'] is not None and api_ready is not None and auth_ok:break
  # Ubuntu may advance from the persisted timesync clock despite a corrected RTC.
  if a.product.startswith('k3s') and not clock_step and time.time()-corrected>120:
   epoch=int(time.time());cmd=f'sudo date -u -s @{epoch}'
   typed([str(scripts/'dut-command.sh'),cmd]);r=subprocess.run([str(scripts/'dut-command.sh'),cmd],capture_output=True,text=True,timeout=15)
   (d/'manual-time-correction.txt').write_text(r.stdout+r.stderr);manual.append('SSH sudo date to observer UTC after RTC-only recovery did not complete in 120 s');clock_step=True
  time.sleep(5)
 metrics['api_readyz_recovery_seconds']=api_ready
 gateway(f'touch /home/observer/{name}.stop');time.sleep(4);pull()
 with (d/'records').open('wb') as f:subprocess.run([G,'curl --fail --max-time 10 http://10.78.0.11:30081/records'],stdout=f,check=True,timeout=20)
 r=subprocess.run(['python3',str(scripts/'check-records.py'),str(d/'observer.jsonl'),str(d/'records')],text=True,capture_output=True);metrics.update(json.loads(r.stdout))
 if r.returncode:errors.append('acknowledged record loss or corruption')
 dates=[];authenticated=[];diagnostics=[]
 for line in (d/'observer.jsonl').read_text().splitlines():
  row=json.loads(line)
  if row.get('kind')=='api' and row.get('ok') and datetime.datetime.fromisoformat(row['utc']).timestamp()>=corrected:authenticated.append(datetime.datetime.fromisoformat(row['utc']).timestamp()-corrected)
  if row.get('response_date') and row.get('utc') and wrong_started<=datetime.datetime.fromisoformat(row['utc']).timestamp()<corrected:
   dates.append({'observer_utc':row['utc'],'guest_http_date':row['response_date']})
 (d/'guest-http-clock.json').write_text(json.dumps(dates[::max(1,len(dates)//30)],indent=2))
 metrics['guest_http_clock_samples']=len(dates)
 metrics['authenticated_api_recovery_seconds']=min(authenticated) if authenticated else None
 diagnosis=(d/'wrong-rtc-serial.log').read_text(errors='replace')+'\n'+json.dumps(rows)
 metrics['explicit_certificate_diagnosis']=any(word in diagnosis.lower() for word in ['expired','not yet valid','certificate verify failed'])
 if not authenticated:errors.append('authenticated ConfigMap API did not recover')
 elif min(authenticated)>300:errors.append('authenticated API recovery exceeded 300 seconds')
 metrics['strict_tls_api_errors_during_wrong_rtc']=sum(r['phase']=='wrong-RTC' and r['returncode']!=0 for r in rows)
 recovery=metrics.get('recovery_seconds_first_60s_sustained_success')
 if api_ready is None or api_ready>300 or recovery is None or recovery>300:errors.append('API/W recovery exceeded 300 seconds after RTC correction')
 if metrics.get('driver_overloads'):errors.append('external observer overload')
except Exception as exc:errors.append(str(exc))
finally:
 if observer:
  try:gateway(f'touch /home/observer/{name}.stop',check=False);pull()
  except Exception:pass
 try:command(str(d/'qmp.sock'),'quit')
 except Exception:pass
 if recorder:
  (d/'serial.stop').touch();recorder.wait(timeout=10)
  if vga:vga.wait(timeout=60)
  subprocess.run(['python3',str(scripts/'render-serial.py'),str(d/'serial.cast')],check=True)
 subprocess.run([str(scripts/'wan-impairment.sh'),'clean'],check=True)
record={'scenario_id':'E6','product':a.product,'source_commit':os.environ['SOURCE_COMMIT'],'image_digest':os.environ['IMAGE_DIGEST'],'fault_seed':None,'fault_phase':a.case,'rtc_requested_utc':rtc.isoformat(),'ca_expiry':ca,'vcpu':2,'ram_mib':4096,'topology':'fresh raw + OVMF clone; RTC fault; WAN/NTP blackholed in gateway; external strict-TLS observer','metrics':metrics,'manual_actions':manual,'acknowledged_record_loss':metrics.get('acknowledged_record_loss'),'outage_seconds':None,'recovery_seconds':metrics.get('recovery_seconds_first_60s_sustained_success'),'status':'failed' if errors else 'passed-TLS-W','reason':'; '.join(errors) if errors else 'strict TLS preserved and API/W recovered after documented time correction','time_utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'artifact_paths':[str(d)],'measurement_notes':['RTC request is not assumed equal to guest wall time: HTTP Date, mutable-guest date, serial/VGA and API errors retain observations.','No insecure TLS flag or client clock change is used.']}
(d/'result.json').write_text(json.dumps(record,indent=2));print(json.dumps(record),flush=True)
if errors:subprocess.run(['bash',str(scripts/'preserve-failed-disk.sh'),str(d/'system.raw'),str(a.golden)],check=True)
else:(d/'system.raw').unlink()
