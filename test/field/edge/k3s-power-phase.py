#!/usr/bin/env python3
"""Verified APT phases, never described as inactive-slot/probation in k3s."""
import json,os,pathlib,shlex,subprocess,sys,time
from qmp import command
phase=sys.argv[1];assert phase in ['download','package-write','installed-before-reboot','early-boot']
s=pathlib.Path(__file__).resolve().parent;root=pathlib.Path('/work/field-edge');d=pathlib.Path(os.environ['VM_DIR']);delay=float(os.environ['FIELD_PHASE_DELAY'])
def dut(cmd,timeout=20,input=None):
 print('$ '+shlex.join([str(s/'dut-command.sh'),cmd]),flush=True)
 r=subprocess.run([str(s/'dut-command.sh'),cmd],input=input,text=True,capture_output=True,timeout=timeout)
 if r.returncode:raise RuntimeError(r.stderr)
 return r.stdout
key=(root/'k3s-fault-build/trusted-public.gpg').read_bytes()
subprocess.run([str(s/'dut-command.sh'),'sudo tee /usr/share/keyrings/field-test.gpg >/dev/null'],input=key,check=True,capture_output=True)
dut('sudo tee /etc/apt/sources.list.d/field-test.list >/dev/null',input='deb [signed-by=/usr/share/keyrings/field-test.gpg] http://10.0.2.2:15007/good /\n')
options='-o Dir::Etc::sourcelist=/etc/apt/sources.list.d/field-test.list -o Dir::Etc::sourceparts=- -o APT::Get::List-Cleanup=0'
dut('sudo apt-get '+options+' update',timeout=120)
before_boot=dut('cat /proc/sys/kernel/random/boot_id').strip()
if phase=='download':subprocess.run(['bash',str(s/'wan-impairment.sh'),'slow'],check=True)
if phase=='package-write':command(str(d/'qmp.sock'),'qom-set',{'path':'/objects/field-throttle','property':'limits','value':{'bps-total':1024**2}})
cmd='sudo env DEBIAN_FRONTEND=noninteractive apt-get '+options+' install -y field-edge-upgrade'
print('$ '+shlex.join([str(s/'dut-command.sh'),cmd]),flush=True)
process=subprocess.Popen([str(s/'dut-command.sh'),cmd],stdout=(d/'apt-power-upgrade.txt').open('w'),stderr=subprocess.STDOUT)
try:
 if phase in ['installed-before-reboot','early-boot']:
  if process.wait(timeout=360):raise RuntimeError('APT install failed before target phase')
  digest=dut('sha256sum /usr/local/bin/k3s').split()[0]
  assert digest=='265b3ee6a3198ef14033c63ef46cbb32370d83795199fb6d1b01f21575eddbda'
  if phase=='early-boot':
   subprocess.run([str(s/'dut-command.sh'),'sudo reboot'],timeout=15,capture_output=True)
 def observed():
  if phase=='download':
   text=dut("sudo find /var/cache/apt/archives/partial -type f -name '*field-edge-upgrade*' -printf '%s %f\\n'")
   total=(root/'k3s-fault-build/public/good/pool/field-edge-upgrade_1.37.2_amd64.deb').stat().st_size
   return any(0<int(line.split()[0])<total for line in text.splitlines()),{'partial_files':text,'package_bytes':total}
  if phase=='package-write':
   text=dut("sudo stat -c '%s %n' /usr/local/bin/k3s.dpkg-new 2>/dev/null || true")
   size=(root/'k3s-upgrade-a/k3s-target').stat().st_size
   return bool(text) and 0<int(text.split()[0])<size,{'dpkg_new':text,'binary_bytes':size}
  if phase=='installed-before-reboot':return True,{'verified_binary_sha256':digest,'boot_id':before_boot,'automatic_reboot':False}
  text=dut('cat /proc/sys/kernel/random/boot_id; cat /proc/uptime')
  lines=text.splitlines();return lines[0]!=before_boot and float(lines[1].split()[0])<120,{'boot_id':lines[0],'uptime':lines[1],'native_probation':False}
 end=time.monotonic()+360
 while time.monotonic()<end:
  try:
   ok,value=observed()
   if ok:
    first=time.time();time.sleep(delay);ok,value=observed()
    if not ok:raise RuntimeError('phase ended before seeded offset')
    command(str(d/'qmp.sock'),'stop');pause=time.time();assert command(str(d/'qmp.sock'),'query-status')['status']=='paused'
    evidence={'phase':phase,'first_observation_epoch':first,'pause_epoch':pause,'seeded_offset_seconds':delay,'actual_offset_seconds':pause-first,'before_pause':value,'packaging':'unchanged official k3s binary in laboratory APT package; no native inactive OS slot or probation exists in this lane'}
    (d/'phase-evidence.json').write_text(json.dumps(evidence,indent=2));print(json.dumps(evidence),flush=True);break
  except (RuntimeError,subprocess.TimeoutExpired) as exc:
   if 'phase ended' in str(exc):raise
  time.sleep(.1)
 else:raise RuntimeError('real requested APT phase was not observed')
finally:
 if phase=='download':subprocess.run(['bash',str(s/'wan-impairment.sh'),'clean'],check=True)
 # SSH can remain blocked while QEMU is frozen; terminate only its local client.
 if process.poll() is None:process.terminate();process.wait(timeout=10)
