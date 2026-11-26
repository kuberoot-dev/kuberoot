#!/usr/bin/env python3
"""Real APT trust/integrity and k3s A→B trials with declared lab fault packaging."""
import argparse,datetime,hashlib,json,os,pathlib,shlex,shutil,subprocess,time
from qmp import command
from upgrade_observations import evaluate as evaluate_service, wait_for_service
p=argparse.ArgumentParser();p.add_argument('--case',choices=['init-crash','pod-network','wrong-content','incompatible-state','tampered','untrusted'],required=True);p.add_argument('--output',type=pathlib.Path,required=True);p.add_argument('--repetition',type=int,required=True);p.add_argument('--deadline',type=int,default=1500);a=p.parse_args()
root=pathlib.Path('/work/field-edge');s=pathlib.Path(__file__).resolve().parent;golden=root/'k3s-upgrade-a';d=a.output;assert d.is_relative_to(root)
assert shutil.disk_usage(root).free>(golden/'system.raw').stat().st_blocks*512+1024**3
d.mkdir();negative=a.case in ['tampered','untrusted'];env=dict(os.environ,VM_DIR=str(d),SEED_MEDIA=str(golden/'seed.iso'),FAULT_FIXTURE='1',KUBECONFIG=str(golden/'kubeconfig'),DUT_KNOWN_HOSTS=str(golden/'known_hosts'))
for file in ['system.raw','vars.fd']:subprocess.run(['cp','--sparse=always',str(golden/file),str(d/file)],check=True)
G=str(s/'gateway-command.sh');C=str(s/'dut-command.sh');metrics={'packaging':'lab APT package: unchanged official k3s B binary plus intentional systemd fault; positive cases also request pinned native Ubuntu linux-image-virtual candidate','target_binary_sha256':'265b3ee6a3198ef14033c63ef46cbb32370d83795199fb6d1b01f21575eddbda'};errors=[];fault_verified=False;recorder=None;name=d.name
metrics['ubuntu_cloud_image_sha256']='04e8f2461a16a44e78a370284471f29616cdd9baf1893df25807b71f985b6c94'
metrics['a_binary_sha256']='92b94582eb7b34a8cf532e6006842b9ed215a6783cf56a3dd6271fd35243b2c0'
repo=root/'k3s-fault-build/public'/a.case
metrics['lab_package_provenance']={}
for relative in ['pool/field-edge-trial_0.1.0_amd64.deb','InRelease','Packages']:
 with (repo/relative).open('rb') as source:metrics['lab_package_provenance'][relative]=hashlib.file_digest(source,'sha256').hexdigest()


def typed(args):
 print('$ ',end='',flush=True)
 for ch in shlex.join([str(x) for x in args]):print(ch,end='',flush=True);time.sleep(.01)
 print(flush=True)
def dut(cmd,visible=False,timeout=30,input=None):
 if visible:typed([C,cmd])
 r=subprocess.run([C,cmd],env=env,input=input,text=True,capture_output=True,timeout=timeout)
 if visible:print(r.stdout+r.stderr,end='',flush=True)
 return r
def gateway(cmd):return subprocess.run([G,cmd],text=True,capture_output=True,timeout=30)
def kube(*args):return subprocess.run(['kubectl','--request-timeout=5s',*args],env=env,text=True,capture_output=True,timeout=8)
def inventory():
 r=dut('cat /proc/sys/kernel/random/boot_id; /usr/local/bin/k3s --version; sha256sum /usr/local/bin/k3s; sudo find /boot -type f -exec sha256sum {} + | sort')
 if r.returncode:raise RuntimeError(r.stderr)
 return r.stdout
try:
 subprocess.run(['bash','-c','source "$1/lib.sh"; vm_start 1','_',str(s)],env=env,check=True)
 for _ in range(150):
  r=gateway('curl --silent --max-time 2 http://10.78.0.11:30080/http')
  if r.returncode==0 and r.stdout=='W'*1024 and kube('get','--raw','/readyz').returncode==0:break
  time.sleep(2)
 else:raise RuntimeError('A baseline did not become ready')
 metrics['kernel_before']=dut('uname -r').stdout.strip()
 before=inventory();(d/'package-before.txt').write_text(before);before_boot=before.splitlines()[0];metrics['boot_id_before']=before_boot
 if 'v1.37.1+k3s1' not in before:raise RuntimeError('unexpected A version')
 subprocess.run(['bash',str(s/'prepare-observer.sh')],env=env,check=True)
 # Only the known laboratory repository key is trusted; foreign key stays absent.
 key=(root/'k3s-fault-build/trusted-public.gpg').read_bytes()
 r=subprocess.run([C,'sudo tee /usr/share/keyrings/field-test.gpg >/dev/null'],env=env,input=key,capture_output=True,timeout=15)
 if r.returncode:raise RuntimeError('could not install laboratory repository public key')
 source=f'deb [signed-by=/usr/share/keyrings/field-test.gpg] http://10.0.2.2:15007/{a.case} /\n'
 r=dut('sudo tee /etc/apt/sources.list.d/field-test.list >/dev/null',input=source)
 if r.returncode:raise RuntimeError(r.stderr)
 options='-o Dir::Etc::sourcelist=/etc/apt/sources.list.d/field-test.list -o Dir::Etc::sourceparts=- -o APT::Get::List-Cleanup=0'
 typed(['export','KUBECONFIG='+env['KUBECONFIG']]);typed(['kubectl','-n','field','get','pods']);print(kube('-n','field','get','pods').stdout,flush=True)
 gateway(f'nohup python3 /home/observer/observer.py --http http://10.78.0.11:30080 --append http://10.78.0.11:30081 --kubeconfig /home/observer/kubeconfig --seconds {a.deadline+400} --stop-file /home/observer/{name}.stop --output /home/observer/{name}.jsonl >/home/observer/{name}.log 2>&1 </dev/null &')
 time.sleep(10)
 serial=shlex.join(['python3',str(s/'serial-follow.py'),str(d/'serial.log'),'--from-end','--stop-file',str(d/'serial.stop'),'--max-seconds',str(a.deadline+400)])
 recorder=subprocess.Popen(['asciinema','rec','--cols','110','--rows','30','-q','-c',serial,str(d/'serial.cast')],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
 typed(['date','-u']);subprocess.run(['date','-u']);start=time.monotonic();metrics['fault_epoch']=time.time()
 update=dut('sudo apt-get '+options+' update',visible=True,timeout=120)
 (d/'apt-update.txt').write_text(update.stdout+update.stderr)
 if not negative:
  native_update=dut('sudo apt-get update',visible=True,timeout=180)
  (d/'apt-native-update.txt').write_text(native_update.stdout+native_update.stderr)
  if native_update.returncode:raise RuntimeError('Native Ubuntu APT metadata refresh failed')
 if a.case=='untrusted':
  output=update.stdout+update.stderr
  metrics['apt_update_exit']=update.returncode
  if update.returncode==0 or not any(x in output for x in ['NO_PUBKEY','not signed','Missing key','couldn\'t be verified']):errors.append('APT did not demonstrate foreign signer rejection')
 else:
  if update.returncode:raise RuntimeError('trusted repository update failed: '+update.stderr)
  kernel=(golden/'kernel-target-version').read_text().strip()
  if not negative and kernel in ['', '(none)']:raise RuntimeError('No native Ubuntu kernel candidate captured')
  install_command='sudo env DEBIAN_FRONTEND=noninteractive apt-get '+(options+' install -y field-edge-trial' if negative else 'install -y field-edge-trial '+shlex.quote('linux-image-virtual='+kernel))
  metrics['requested_native_kernel_package']=None if negative else 'linux-image-virtual='+kernel
  install=dut(install_command,visible=True,timeout=600)
  (d/'apt-install.txt').write_text(install.stdout+install.stderr);metrics['apt_install_exit']=install.returncode
  if a.case=='tampered':
   if install.returncode==0 or 'Hash Sum mismatch' not in install.stdout+install.stderr:errors.append('APT did not demonstrate corrupted package rejection')
  elif install.returncode:raise RuntimeError('trusted positive fault package did not install: '+install.stderr)
 if not negative:
  reboot_request=dut('sudo reboot',visible=True,timeout=15)
  metrics['management_client']='APT and reboot SSH commands ended before target-boot observation; no further update/recovery commands; WAN available'
  metrics['reboot_client_exit']=reboot_request.returncode
  target_seen=False
  with (d/'observations.jsonl').open('x') as out:
   while time.monotonic()-start<a.deadline:
    row={'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'elapsed_seconds':time.monotonic()-start}
    try:
     r=dut('cat /proc/sys/kernel/random/boot_id; /usr/local/bin/k3s --version; uname -r; systemctl is-active field-fault; sudo journalctl -u field-fault -n 12 --no-pager',timeout=15)
     row['status']=r.stdout;row['ssh_exit']=r.returncode
     if 'v1.37.2+k3s1' in r.stdout and r.stdout.splitlines()[0]!=before_boot:
      if 'kernel_after' not in metrics:
       metrics['kernel_after']=dut('uname -r').stdout.strip();metrics['kernel_changed']=metrics['kernel_after']!=metrics['kernel_before']
      target_seen=True
      expected={'init-crash':'intentional init-service crash','pod-network':'injected pod-network drop rules','wrong-content':'injected wrong-content NAT rule','incompatible-state':'test state migrated incompatibly'}[a.case]
      if expected in r.stdout:
       if a.case=='wrong-content':
        probe=gateway('curl --silent --max-time 2 http://10.78.0.11:30080/http');fault_verified=probe.returncode==0 and probe.stdout=='X'*1024
       elif a.case=='pod-network':
        probe=gateway('curl --silent --max-time 2 http://10.78.0.11:30080/http');fault_verified=probe.returncode!=0 and kube('get','--raw','/readyz').returncode==0
       else:fault_verified=True
       (d/'fault-service.log').write_text(r.stdout)
     elif target_seen and 'v1.37.1+k3s1' in r.stdout:
      metrics['automatic_rollback_seconds']=row['elapsed_seconds'];break
     row['fault_verified']=fault_verified
    except Exception as exc:row['error']=str(exc)
    print(json.dumps(row),flush=True);out.write(json.dumps(row)+'\n');out.flush();time.sleep(10)
  metrics['target_boot_observed']=target_seen;metrics['fault_verified']=fault_verified
  if not fault_verified:errors.append('injected fault not verified; no product conclusion')
  if 'automatic_rollback_seconds' not in metrics:errors.append('no automatic return to k3s A within 1500 seconds')
 else:
  after=inventory();(d/'package-after.txt').write_text(after);metrics['boot_and_binary_unchanged']=after==before
  if after!=before:errors.append('negative package changed boot ID, kernel/boot files or k3s binary')
 settle_metrics,settle_errors=wait_for_service(gateway,name);metrics.update(settle_metrics);errors.extend(settle_errors);gateway(f'touch /home/observer/{name}.stop');time.sleep(4)
 (d/'observer.jsonl').write_text(gateway(f'cat /home/observer/{name}.jsonl').stdout)
 service_metrics,service_errors=evaluate_service(d/'observer.jsonl');metrics.update(service_metrics);errors.extend(service_errors)
 r=gateway('curl --fail --max-time 20 http://10.78.0.11:30081/records')
 if r.returncode==0:
  (d/'records').write_text(r.stdout)
  check=subprocess.run(['python3',str(s/'check-records.py'),str(d/'observer.jsonl'),str(d/'records')],text=True,capture_output=True)
  metrics.update(json.loads(check.stdout))
  if check.returncode:errors.append('acknowledged records lost or corrupted')
 else:errors.append('cannot read durable records after trial')
 typed(['date','-u']);subprocess.run(['date','-u'])
except Exception as exc:errors.append(str(exc))
finally:
 subprocess.run(['bash',str(s/'wan-impairment.sh'),'clean'],check=False)
 gateway(f'touch /home/observer/{name}.stop')
 try:command(str(d/'qmp.sock'),'quit')
 except Exception:pass
 if recorder:
  (d/'serial.stop').touch();recorder.wait(timeout=15)
  timing=json.loads(subprocess.check_output(['python3','/work/scripts/render.py',str(d/'serial.cast')],text=True))
  subprocess.run(['python3',str(s/'bound-cast.py'),str(d/'serial.retimed.cast')],check=True,stdout=(d/'serial-speed.json').open('w'))
  subprocess.run(['agg','-q','--font-size','16','--theme','monokai','--last-frame-duration',str(max(5,15.1-timing['playback_seconds'])),str(d/'serial.retimed.cast'),str(d/'serial.gif')],check=True)
record={'measurement_protocol':'upgrade-client-disconnect-v2','scenario_id':'E2','product':'k3s-v1.37.1-to-v1.37.2-lab-package','source_commit':'f6973cb850e9b245585363d15bde80904ac862fd','image_digest':'sha256:'+metrics['target_binary_sha256'],'repetition':a.repetition,'fault_seed':None,'fault_phase':a.case,'vcpu':2,'ram_mib':4096,'topology':'fresh Ubuntu/k3s A raw+UEFI; actual signed APT test package; multicast/gateway','metrics':metrics,'outage_seconds':None,'recovery_seconds':metrics.get('automatic_rollback_seconds'),'acknowledged_record_loss':metrics.get('acknowledged_record_loss'),'manual_actions':[],'status':'invalid-fixture' if (not negative and not fault_verified) or metrics.get('driver_overloads') else 'failed' if errors else 'passed','reason':'; '.join(errors) if errors else 'APT rejected negative package and retained boot state and acknowledged records','time_utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'artifact_paths':[str(d)]}
(d/'result.json').write_text(json.dumps(record,indent=2));print(json.dumps(record),flush=True)
