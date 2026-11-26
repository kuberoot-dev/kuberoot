#!/usr/bin/env python3
"""Attempt actual blank worker enrollment with a pre-loss unused membership.
The restored server stays running; the old source disk is never consulted.
"""
import datetime,json,os,pathlib,shlex,subprocess,time
from qmp import command
root=pathlib.Path('/work/field-edge');s=pathlib.Path(__file__).resolve().parent;parent=pathlib.Path(os.environ['CASE_OUTPUT']);d=parent/'credential-clone';d.mkdir();fixture=root/'restore-fixture';env=dict(os.environ,VM_DIR=str(d));node_env=dict(os.environ,KUBECONFIG=str(root/'kuberoot-main/trust/admin.kubeconfig'));tunnel=recorder=None;events=[];joined=False;rejected=False

def call(args,**kw):
 print('$ '+shlex.join([str(x) for x in args]),flush=True)
 return subprocess.run([str(x) for x in args],text=True,capture_output=True,**kw)
def node(*args):return call(['kubectl','--server=https://127.0.0.1:15002','--request-timeout=5s',*args],env=node_env,timeout=8)
def kube(*args):return call(['kubectl','--request-timeout=5s',*args],timeout=8)
try:
 # Reserve the second MAC explicitly, identically on the existing userspace LAN.
 r=call([s/'gateway-command.sh',"printf '%s\\n' 'dhcp-host=52:54:00:78:00:02,10.78.0.12' | sudo tee /etc/dnsmasq.d/field-clone.conf >/dev/null; sudo systemctl restart dnsmasq"],timeout=20);assert r.returncode==0
 tunnel=subprocess.Popen(['ssh','-o','BatchMode=yes','-o','ExitOnForwardFailure=yes','-o','StrictHostKeyChecking=accept-new','-o','UserKnownHostsFile='+str(root/'gateway/known_hosts'),'-i',str(root/'gateway/key'),'-p','2222','-N','-L','127.0.0.1:15002:10.78.0.12:50000','observer@127.0.0.1'],stdout=subprocess.DEVNULL,stderr=(d/'tunnel.log').open('w'))
 call(['bash',s/'boot-node.sh','2'],env=dict(env,SEED_MEDIA=str(fixture/'restore-media.img')),check=True)
 for _ in range(150):
  if node('get','disks').returncode==0:break
  time.sleep(2)
 else:raise RuntimeError('blank clone live API unavailable')
 request={'apiVersion':'node.kuberoot.dev/v1alpha1','kind':'Installation','metadata':{'name':'clone-install'},'spec':{'disk':'vdb','reboot':True}}
 r=call(['kubectl','--server=https://127.0.0.1:15002','create','-f','-'],env=node_env,input=json.dumps(request),timeout=15);assert r.returncode==0,r.stderr
 for _ in range(200):
  if 'reboot: Restarting system' in (d/'serial.log').read_text(errors='replace'):break
  time.sleep(3)
 else:raise RuntimeError('clone installation did not finish')
 command(str(d/'qmp.sock'),'quit');time.sleep(2);(d/'install-serial.log').write_bytes((d/'serial.log').read_bytes())
 call(['bash',s/'boot-node.sh','2'],env=env,check=True)
 for _ in range(150):
  if node('get','bootentries').returncode==0:break
  time.sleep(2)
 else:raise RuntimeError('installed clone API unavailable')
 serial=shlex.join(['python3',str(s/'serial-follow.py'),str(d/'serial.log'),'--from-end','--stop-file',str(d/'serial.stop'),'--max-seconds','700'])
 recorder=subprocess.Popen(['asciinema','rec','--cols','110','--rows','30','-q','-c',serial,str(parent/'clone.cast')],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
 start=time.monotonic();r=node('apply','-f',str(fixture/'private/preloss-membership.yaml'))
 events.append({'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'apply_exit':r.returncode,'diagnosis':r.stderr})
 for _ in range(120):
  r=kube('get','node','kuberoot-780002','-o','json')
  if r.returncode==0:
   n=json.loads(r.stdout);events.append({'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'node_name':n['metadata']['name'],'uid':n['metadata']['uid'],'conditions':n.get('status',{}).get('conditions',[])})
   joined=True;break
  # The node's logs are actual evidence. A timeout alone is not rejection proof.
  log=(d/'serial.log').read_text(errors='replace')
  rejected=any(term in log for term in ['Unauthorized','bootstrap token is invalid','token has expired'])
  if rejected:break
  time.sleep(5)
 result={'preloss_credential_replay_joined':joined,'clone_rejected':rejected and not joined,'observation_seconds':time.monotonic()-start,'replay_node':'kuberoot-780002','replay_mac':'52:54:00:78:00:02','credential_scope':'unused pre-loss Worker Membership, including bootstrap token and node API certificate; no assertion about every credential class','events':events}
except Exception as exc:result={'preloss_credential_replay_joined':joined,'clone_rejected':False,'fixture_error':str(exc),'events':events}
finally:
 try:command(str(d/'qmp.sock'),'quit')
 except Exception:pass
 if tunnel:tunnel.terminate();tunnel.wait(timeout=10)
 if recorder:
  (d/'serial.stop').touch();recorder.wait(timeout=15);subprocess.run(['python3',str(s/'render-serial.py'),str(parent/'clone.cast')],check=True)
(parent/'credential-replay.json').write_text(json.dumps(result,indent=2));print(json.dumps(result),flush=True)
