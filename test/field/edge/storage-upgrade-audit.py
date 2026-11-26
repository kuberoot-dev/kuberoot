#!/usr/bin/env python3
"""Observe actual native update outcome while a storage fault is active.
A failure to reach the API is not counted as proof of a safe rejection.
"""
import datetime,json,os,pathlib,shlex,subprocess,time
root=pathlib.Path('/work/field-edge');s=pathlib.Path(__file__).resolve().parent;d=pathlib.Path(os.environ['CASE_OUTPUT']);product=os.environ['PRODUCT'];kind=os.environ['CASE'];env=dict(os.environ);process=None;error=None;rows=[];start=time.time();created=False;terminal=None;success_during_fault=False

def stamp():return datetime.datetime.now(datetime.timezone.utc).isoformat()
def run(args,**kw):
 print('$ '+shlex.join([str(x) for x in args]),flush=True);r=subprocess.run([str(x) for x in args],text=True,capture_output=True,**kw);print(r.stdout+r.stderr,end='',flush=True);return r
try:
 if product.startswith('kuberoot'):
  env['KUBECONFIG']=os.environ['NODE_KUBECONFIG']
  request={'apiVersion':'node.kuberoot.dev/v1alpha1','kind':'Upgrade','metadata':{'name':'field-storage-upgrade'},'spec':{'url':os.environ['GOOD_BUNDLE_URL'],'sha256':os.environ['GOOD_BUNDLE_SHA256'],'reboot':False}}
  p=d/'storage-upgrade-request.json';p.write_text(json.dumps(request,indent=2));r=run(['kubectl','--request-timeout=10s','create','-f',p],env=env,timeout=15);created=r.returncode==0
  if not created:
   if any(x in (r.stdout+r.stderr).lower() for x in ['no space left','input/output error','read-only file system']):terminal='explicit-storage-rejection'
   else:error='Upgrade request not accepted, without an explicit storage-error diagnosis'
 elif product.startswith('k3s'):
  cmd='sudo env DEBIAN_FRONTEND=noninteractive apt-get -o Dir::Etc::sourcelist=/etc/apt/sources.list.d/field-test.list -o Dir::Etc::sourceparts=- install -y field-edge-upgrade'
  print('$ '+shlex.join([str(s/'dut-command.sh'),cmd]),flush=True);process=subprocess.Popen([str(s/'dut-command.sh'),cmd],stdout=(d/'storage-upgrade-native.txt').open('w'),stderr=subprocess.STDOUT);created=True
 else:
  import talos_trial as t
  process=t.upgrade(d,no_reboot=True);created=True
 with (d/'storage-upgrade-observations.jsonl').open('x') as out:
  while created and time.time()-start<900:
   active=not (d/'relief-epoch').exists();row={'utc':stamp(),'fault_active':active,'elapsed_seconds':time.time()-start}
   if product.startswith('kuberoot'):
    r=run(['kubectl','--request-timeout=5s','get','upgrade','field-storage-upgrade','-o','json'],env=env,timeout=8)
    if r.returncode==0:
     obj=json.loads(r.stdout);row['status']=obj.get('status',{});phase=row['status'].get('phase')
     if phase in ['Staged','Failed']:terminal=phase
    else:row['api_error']=r.stderr
   else:
    exitcode=process.poll();row['native_exit']=exitcode
    if exitcode is not None:terminal='succeeded' if exitcode==0 else 'failed'
   row['terminal']=terminal
   if active and terminal in ['Staged','succeeded']:success_during_fault=True
   out.write(json.dumps(row)+'\n');out.flush();rows.append(row)
   if terminal:break
   if not active and time.time()-float((d/'relief-epoch').read_text())>180:break
   time.sleep(5)
except Exception as exc:error=str(exc)
finally:
 if process and process.poll() is None:process.terminate();process.wait(timeout=15)
result={'request_started':created,'terminal_outcome':terminal,'success_while_fault_active':success_during_fault,'false_success_under_all_disk_eio':kind=='eio' and success_during_fault,'requires_boot_validation':kind=='full-100' and success_during_fault,'explicit_storage_rejection':terminal=='explicit-storage-rejection','error':error,'native_operation':'kuberoot staged signed bundle' if product.startswith('kuberoot') else 'lab APT package with unchanged official k3s binary' if product.startswith('k3s') else 'native Talos --no-reboot installer','note':'ENOSPC on state alone may leave EFI/root storage writable. Success is not called false merely because state is full; such success requires a real subsequent boot check.'}
(d/'storage-upgrade-result.json').write_text(json.dumps(result,indent=2));print(json.dumps(result),flush=True)
