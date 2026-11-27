#!/usr/bin/env python3
"""Inject EIO into the allocated block of the actual persisted nft configuration."""
import json,os,re,signal,subprocess,time
from pathlib import Path
raw=Path('/work/field/raw');rows=[]
while not (raw/'R11-debian-storage.exit').exists():time.sleep(3)
def g(vm,cmd,timeout=40):
 print(time.strftime('%FT%TZ',time.gmtime()),'$ guest-command',vm,cmd,flush=True)
 p=subprocess.run(['/work/field/guest-command',vm,cmd],text=True,capture_output=True,timeout=timeout);print(p.stdout+p.stderr,flush=True);rows.append({'at':time.time(),'vm':vm,'command':cmd,'exit':p.returncode,'stdout':p.stdout,'stderr':p.stderr});return p
def boot_wait():
 start=time.monotonic()
 while time.monotonic()-start<150:
  if g('debian','true').returncode==0:return
  time.sleep(2)
 raise RuntimeError('guest did not boot')
def argv():return [x.decode() for x in Path('/proc/'+Path('/work/vms/debian/pid').read_text().strip()+'/cmdline').read_bytes().split(b'\0') if x]
def restart(args,hard=False):
 pid=int(Path('/work/vms/debian/pid').read_text())
 if hard:os.kill(pid,signal.SIGKILL)
 else:subprocess.run(['python3','/work/field/qmp.py','debian','quit'],check=True)
 for _ in range(50):
  try:
   if Path(f'/proc/{pid}/stat').read_text().split(')')[1].split()[0]=='Z':break
  except FileNotFoundError:break
  time.sleep(.1)
 Path('/work/vms/debian/pid').unlink(missing_ok=True)
 for _ in range(10):
  p=subprocess.run(args,capture_output=True,text=True)
  if p.returncode==0:break
  if 'lock' not in p.stderr:raise RuntimeError(p.stderr)
  time.sleep(1)
 else:raise RuntimeError('image lock not released')
 boot_wait()
g('debian',"grep -q virtio-fieldstate /etc/fstab || printf '/dev/disk/by-id/virtio-fieldstate /var/lib/field-config ext4 defaults 0 0\\n' >>/etc/fstab; sync")
# A genuinely acknowledged durable write, followed immediately by a hard stop.
marker=str(time.time());cmd=f"python3 -c 'import os; p=\"/var/lib/field-config/rules.nft\"; f=open(p,\"a\"); f.write(\"# acknowledged-{marker}\\n\"); f.flush(); os.fsync(f.fileno()); print(\"ACK {marker}\")'"
p=g('debian',cmd)
if p.returncode:raise SystemExit('no acknowledged write; no power-cut claim')
args=argv();cut=time.time();restart(args,hard=True);p=g('debian','cat /var/lib/field-config/rules.nft');retained='acknowledged-'+marker in p.stdout
(raw/'R11-debian-acked-powercut.summary.json').write_text(json.dumps({'acked_marker':marker,'retained':retained,'cut_at':cut,'observed_recovery_s':time.time()-cut},indent=2)+'\n')
p=g('debian','sync; filefrag -v -b512 /var/lib/field-config/rules.nft')
(raw/'R11-debian-file-extents.txt').write_text(p.stdout+p.stderr)
m=re.search(r'^\s*0:\s*0\.\.\s*\d+:\s*(\d+)\.\.',p.stdout,re.M)
if not m:raise SystemExit('cannot locate config block; no fabricated EIO')
sector=int(m[1]);conf=Path('/work/field/R11-eio.conf');conf.write_text(f'[inject-error]\nevent = "write_aio"\nerrno = "5"\nsector = "{sector}"\nonce = "off"\nimmediately = "off"\n')
args=argv();faultargs=[x.replace('file=/work/vms/debian/field-state.raw','file=blkdebug:/work/field/R11-eio.conf:/work/vms/debian/field-state.raw') for x in args]
if args==faultargs:raise SystemExit('state device not in launch argv; need alternate attachment')
g('debian','sync');restart(faultargs)
g('client','nohup ping -D -i .1 -w 310 198.19.0.1 >/tmp/R11-eio-forwarding.txt 2>&1 </dev/null &')
start=time.monotonic();events=[]
while time.monotonic()-start<300:
 p=g('debian',"python3 -c 'import os; p=\"/var/lib/field-config/rules.nft\"; f=open(p,\"r+\"); data=f.read(); f.seek(0); f.write(data); f.flush(); os.fsync(f.fileno()); print(\"ACK rewrite\")'",40)
 events.append({'at':time.time(),'elapsed_s':time.monotonic()-start,'exit':p.returncode,'stdout':p.stdout,'stderr':p.stderr});time.sleep(5)
p=g('client','cat /tmp/R11-eio-forwarding.txt');(raw/'R11-debian-eio-forwarding.txt').write_text(p.stdout+p.stderr)
p=g('debian','dmesg | tail -35; nft list ruleset');(raw/'R11-debian-eio-state.txt').write_text(p.stdout+p.stderr)
(raw/'R11-debian-eio.json').write_text(json.dumps(events,indent=2)+'\n');(raw/'R11-debian-eio-driver.json').write_text(json.dumps(rows,indent=2)+'\n')
print(json.dumps({'sector':sector,'duration_s':time.monotonic()-start,'errors':sum(x['exit']!=0 for x in events),'attempts':len(events),'acked_before_cut_retained':retained}),flush=True)
# Explicit removal of injected backend errors is test cleanup, not automatic recovery.
restart(args);(raw/'R11-debian-eio.exit').write_text('0\n')
