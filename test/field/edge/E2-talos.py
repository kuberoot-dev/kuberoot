#!/usr/bin/env python3
"""Real Talos upgrade trials, with default image policy and explicit fault proof.
Registry CA trust is transport-only; no image-verification policy is added.
A manual rollback is never counted as automatic recovery.
"""
import argparse,datetime,json,os,pathlib,shlex,shutil,subprocess,time
from qmp import command
from upgrade_observations import evaluate as evaluate_service
p=argparse.ArgumentParser();p.add_argument('--golden',type=pathlib.Path,required=True);p.add_argument('--output',type=pathlib.Path,required=True);p.add_argument('--case',choices=['init-crash','pod-network','wrong-content','incompatible-state','tampered','untrusted'],required=True);p.add_argument('--image',required=True);p.add_argument('--repetition',type=int,required=True);p.add_argument('--deadline',type=int,default=1500);a=p.parse_args()
root=pathlib.Path('/work/field-edge');s=pathlib.Path(__file__).resolve().parent;d=a.output
assert d.is_relative_to(root) and a.golden.is_relative_to(root)
assert shutil.disk_usage(root).free>(a.golden/'system.raw').stat().st_blocks*512+1024**3
negative=a.case in ['tampered','untrusted'];d.mkdir();env=dict(os.environ,VM_DIR=str(d),FAULT_FIXTURE='1')
for f in ['system.raw','vars.fd']:subprocess.run(['cp','--sparse=always',str(a.golden/f),str(d/f)],check=True)
name=d.name;metrics={'image_reference':a.image,'image_policy':'Talos product default; only registry transport CA added'};errors=[];fault_verified=False;recorder=vga=upgrade=None;target_seen=False;wan_cut=False
G=str(s/'gateway-command.sh');tc=['/home/observer/talosctl','--talosconfig','/home/observer/talosconfig','--nodes','10.78.0.11','--endpoints','10.78.0.11']
def typed(args):
 print('$ ',end='',flush=True)
 for ch in shlex.join([str(x) for x in args]):print(ch,end='',flush=True);time.sleep(.01)
 print(flush=True)
def gateway(cmd,timeout=30):return subprocess.run([G,cmd],text=True,capture_output=True,timeout=timeout)
def talos(*args,visible=False,timeout=20):
 args=tc+list(args)
 if visible:typed(args)
 r=gateway(shlex.join(args),timeout)
 if visible:print(r.stdout+r.stderr,end='',flush=True)
 return r
def kube(*args):return subprocess.run(['kubectl','--request-timeout=5s',*args],env=env,text=True,capture_output=True,timeout=8)
def snapshot():
 return {kind:talos('get',kind,'-o','yaml').stdout for kind in ['extensions','machinestatus','version']}
def boot_digest():
 command(str(d/'qmp.sock'),'stop')
 try:return json.loads(subprocess.check_output(['python3',str(s/'boot-partition-digests.py'),str(d/'system.raw')],text=True))
 finally:command(str(d/'qmp.sock'),'cont')
try:
 subprocess.run(['bash','-c','source "$1/lib.sh"; vm_start 1','_',str(s)],env=env,check=True)
 for _ in range(150):
  r=kube('get','nodes','-o','json')
  if r.returncode==0:
   before_node=json.loads(r.stdout)['items'][0]
   probe=gateway('curl --fail --max-time 2 http://10.78.0.11:30080/http')
   if probe.returncode==0 and probe.stdout=='W'*1024:break
  time.sleep(2)
 else:raise RuntimeError('golden W not ready')
 before_boot=before_node['status']['nodeInfo']['bootID'];metrics['boot_id_before']=before_boot
 subprocess.run(['bash',str(s/'prepare-observer.sh')],env=env,check=True)
 patch=root/'oci-registry/talos-registry-patch.json';private=d/'machine-config.private.yaml'
 subprocess.run(['talosctl','machineconfig','patch',str(root/'talos/config/controlplane-fixed.yaml'),'--patch','@'+str(patch),'--output',str(private)],check=True,capture_output=True)
 subprocess.run(['scp','-o','BatchMode=yes','-o','StrictHostKeyChecking=accept-new','-o','UserKnownHostsFile='+str(root/'gateway/known_hosts'),'-i',str(root/'gateway/key'),'-P','2222',str(private),'observer@127.0.0.1:/home/observer/field-talos.private.yaml'],check=True,capture_output=True)
 r=talos('apply-config','--mode','no-reboot','--file','/home/observer/field-talos.private.yaml',visible=True,timeout=60)
 if r.returncode:raise RuntimeError('registry CA configuration rejected: '+r.stderr)
 # Boot partition reads happen before the measured workload, with QEMU paused.
 (d/'boot-partitions-before.json').write_text(json.dumps(boot_digest(),indent=2))
 (d/'talos-before.json').write_text(json.dumps(snapshot(),indent=2))
 typed(['export','KUBECONFIG='+env['KUBECONFIG']]);typed(['kubectl','-n','field','get','pods']);print(kube('-n','field','get','pods').stdout,flush=True)
 observer=f'/home/observer/{name}'
 r=gateway(f'nohup python3 /home/observer/observer.py --http http://10.78.0.11:30080 --append http://10.78.0.11:30081 --kubeconfig /home/observer/kubeconfig --seconds {a.deadline+240} --stop-file {observer}.stop --output {observer}.jsonl >{observer}.log 2>&1 </dev/null &')
 if r.returncode:raise RuntimeError(r.stderr)
 time.sleep(10)
 serial=shlex.join(['python3',str(s/'serial-follow.py'),str(d/'serial.log'),'--from-end','--stop-file',str(d/'serial.stop'),'--max-seconds',str(a.deadline+120)])
 recorder=subprocess.Popen(['asciinema','rec','--cols','110','--rows','30','-q','-c',serial,str(d/'serial.cast')],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
 vga=subprocess.Popen(['python3',str(s/'vga-capture.py'),'--qmp',str(d/'qmp.sock'),'--output',str(d/'vga'),'--stop-file',str(d/'serial.stop'),'--seconds',str(a.deadline+120)],stdout=(d/'vga.log').open('w'),stderr=subprocess.STDOUT)
 args=tc+['upgrade','--image',a.image,'--reboot-mode','powercycle','--timeout',str(a.deadline)+'s']
 typed(['date','-u']);subprocess.run(['date','-u']);typed(args)
 start=time.monotonic();metrics['fault_epoch']=time.time()
 upgrade=subprocess.Popen([G,shlex.join(args)],stdout=(d/'upgrade-command.txt').open('w'),stderr=subprocess.STDOUT)
 last=None;finished=False
 with (d/'observations.jsonl').open('x') as out:
  while time.monotonic()-start<a.deadline:
   row={'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'elapsed_seconds':time.monotonic()-start}
   ext=talos('get','extensions','-o','yaml');row['extensions']=ext.stdout;row['extension_error']=ext.stderr
   node=kube('get','nodes','-o','json');row['api_available']=node.returncode==0
   if node.returncode==0:
    node=json.loads(node.stdout)['items'][0];row['boot_id']=node['status']['nodeInfo']['bootID'];row['conditions']=node['status']['conditions']
   if 'field-fault' in ext.stdout and row.get('boot_id')!=before_boot:
    target_seen=True
    if not negative and not wan_cut:
     subprocess.run(['bash',str(s/'wan-impairment.sh'),'blackhole'],check=True);wan_cut=True;metrics['wan_disconnected_after_target_boot']=True
    fault=talos('logs','ext-field-fault');(d/'fault-service.log').write_text(fault.stdout+fault.stderr)
    message={'init-crash':'intentional init-service crash','pod-network':'injected pod-network drop rules','wrong-content':'injected wrong-content NAT rule','incompatible-state':'test state migrated incompatibly'}.get(a.case)
    if message and message in fault.stdout:
     if a.case=='wrong-content':
      r=gateway('curl --silent --max-time 2 http://10.78.0.11:30080/http');fault_verified=r.returncode==0 and r.stdout=='X'*1024
     elif a.case=='pod-network':
      r=gateway('curl --silent --max-time 2 http://10.78.0.11:30080/http');fault_verified=row['api_available'] and r.returncode!=0
     else:fault_verified=True
    if negative:
     errors.append('foreign-signed/corrupt image reached boot instead of pre-selection rejection');finished=True
   elif target_seen and ext.returncode==0 and 'field-fault' not in ext.stdout:
    metrics['automatic_rollback_seconds']=row['elapsed_seconds'];finished=True
   command_status=upgrade.poll();row['upgrade_command_exit']=command_status;row['fault_verified']=fault_verified
   if negative and command_status is not None and not target_seen:
    output=(d/'upgrade-command.txt').read_text();metrics['rejection_output']=output
    if command_status==0:errors.append('negative upgrade reported success')
    elif not any(x in output.lower() for x in ['digest','checksum','signature','verification','unexpected commit digest']):
     errors.append('failure did not establish an integrity or signer rejection')
    else:metrics['integrity_rejection_observed']=True
    finished=True
   summary=(row['api_available'],row.get('boot_id'),target_seen,fault_verified,command_status)
   if summary!=last:print(json.dumps({k:v for k,v in row.items() if k not in ['extensions','conditions']}),flush=True);last=summary
   out.write(json.dumps(row)+'\n');out.flush()
   if finished:break
   time.sleep(5)
 if not finished:errors.append('no automatic rollback within 1500-second observation budget')
 metrics['target_boot_observed']=target_seen;metrics['fault_verified']=fault_verified
 if not negative and not fault_verified:errors.append('injected fault not verified; no product conclusion')
 time.sleep(65);gateway(f'touch {observer}.stop');time.sleep(4)
 (d/'observer.jsonl').write_text(gateway(f'cat {observer}.jsonl').stdout)
 service_metrics,service_errors=evaluate_service(d/'observer.jsonl');metrics.update(service_metrics);errors.extend(service_errors)
 r=gateway('curl --fail --max-time 20 http://10.78.0.11:30081/records')
 if r.returncode==0:
  (d/'records').write_text(r.stdout)
  check=subprocess.run(['python3',str(s/'check-records.py'),str(d/'observer.jsonl'),str(d/'records')],text=True,capture_output=True)
  metrics.update(json.loads(check.stdout))
  if check.returncode:errors.append('acknowledged records lost or corrupted')
 else:errors.append('durable records could not be read after observation')
 (d/'talos-after.json').write_text(json.dumps(snapshot(),indent=2));typed(['date','-u']);subprocess.run(['date','-u'])
except Exception as exc:errors.append(str(exc))
finally:
 subprocess.run(['bash',str(s/'wan-impairment.sh'),'clean'],check=False)
 gateway(f'touch /home/observer/{name}.stop')
 if upgrade and upgrade.poll() is None:upgrade.terminate();upgrade.wait(timeout=10)
 try:command(str(d/'qmp.sock'),'quit')
 except Exception:pass
 time.sleep(2)
 if (d/'boot-partitions-before.json').exists():
  try:
   after=json.loads(subprocess.check_output(['python3',str(s/'boot-partition-digests.py'),str(d/'system.raw')],text=True));(d/'boot-partitions-after.json').write_text(json.dumps(after,indent=2))
   metrics['boot_partitions_unchanged']=after==json.loads((d/'boot-partitions-before.json').read_text())
   if negative and not metrics['boot_partitions_unchanged']:errors.append('boot partitions changed in negative upgrade')
  except Exception as exc:errors.append('boot partition comparison: '+str(exc))
 (d/'serial.stop').touch()
 if recorder:
  recorder.wait(timeout=15)
  timing=json.loads(subprocess.check_output(['python3','/work/scripts/render.py',str(d/'serial.cast')],text=True))
  subprocess.run(['python3',str(s/'bound-cast.py'),str(d/'serial.retimed.cast')],check=True,stdout=(d/'serial-speed.json').open('w'))
  subprocess.run(['agg','-q','--font-size','16','--theme','monokai','--last-frame-duration',str(max(5,15.1-timing['playback_seconds'])),str(d/'serial.retimed.cast'),str(d/'serial.gif')],check=True)
 if vga:vga.wait(timeout=60)
record={'scenario_id':'E2','product':os.environ['PRODUCT'],'source_commit':os.environ['SOURCE_COMMIT'],'image_digest':os.environ['IMAGE_DIGEST'],'repetition':a.repetition,'fault_seed':None,'fault_phase':a.case,'vcpu':2,'ram_mib':4096,'topology':'fresh raw/UEFI; real Talos installer extension; multicast/gateway; default image policy','metrics':metrics,'outage_seconds':None,'recovery_seconds':metrics.get('automatic_rollback_seconds'),'acknowledged_record_loss':metrics.get('acknowledged_record_loss'),'manual_actions':[],'status':'invalid-fixture' if (not negative and not fault_verified) or metrics.get('driver_overloads') else 'failed' if errors else 'passed','reason':'; '.join(errors) if errors else 'automatic rollback or integrity rejection and acknowledged-record gates passed','time_utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'artifact_paths':[str(d)]}
(d/'result.json').write_text(json.dumps(record,indent=2));print(json.dumps(record),flush=True)
