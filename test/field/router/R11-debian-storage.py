#!/usr/bin/env python3
"""Dedicated actual nft config filesystem: IOPS limits, fullness and visible errors."""
import json,subprocess,time,os
from pathlib import Path
raw=Path('/work/field/raw')
while not (raw/'R4-debian-isolation.exit').exists():time.sleep(3)
def qmp(cmd,args):
 print('$ qmp debian',cmd,json.dumps(args),flush=True)
 return subprocess.run(['python3','/work/field/qmp.py','debian',cmd,json.dumps(args)],capture_output=True,text=True,check=True).stdout
def guest(vm,cmd,timeout=60):
 print(time.strftime('%FT%TZ',time.gmtime()),'$ guest-command',vm,cmd,flush=True)
 p=subprocess.run(['/work/field/guest-command',vm,cmd],capture_output=True,text=True,timeout=timeout);print(p.stdout+p.stderr,flush=True);return p
image='/work/vms/debian/field-state.raw'
if Path(image).exists():raise SystemExit('existing storage trial preserved')
subprocess.run(['qemu-img','create','-f','raw',image,'128M'],check=True)
qmp('blockdev-add',{'driver':'file','node-name':'field-state-file','filename':image})
qmp('blockdev-add',{'driver':'raw','node-name':'field-state','file':'field-state-file'})
try:
 qmp('device_add',{'driver':'virtio-blk-pci','drive':'field-state','id':'fieldstate','serial':'fieldstate'})
except subprocess.CalledProcessError as e:
 # Q35 root bus may reject PCI hotplug. Preserve evidence and attach at boot.
 (raw/'R11-debian-hotplug-rejection.txt').write_text(str(e)+'\n'+str(e.stdout)+'\n'+str(e.stderr))
 pid=Path('/work/vms/debian/pid').read_text().strip();argv=Path('/proc/'+pid+'/cmdline').read_bytes().split(b'\0');argv=[x.decode() for x in argv if x]
 guest('debian','sync');qmp('quit',{});time.sleep(3);Path('/work/vms/debian/pid').unlink(missing_ok=True)
 argv+=['-drive','file='+image+',format=raw,if=none,id=field-state,cache=none','-device','virtio-blk-pci,drive=field-state,id=fieldstate,serial=fieldstate']
 subprocess.run(argv,check=True)
 for _ in range(90):
  if guest('debian','true').returncode==0:break
  time.sleep(2)
 else:raise SystemExit('explicit storage-device boot failed')
time.sleep(3)
p=guest('debian','cp -a /var/lib/field-config /root/R11-config-before; cp -a /etc/nftables.conf /root/R11-nft-before; mkfs.ext4 -F /dev/disk/by-id/virtio-fieldstate; mount /dev/disk/by-id/virtio-fieldstate /var/lib/field-config; cp /root/R11-nft-before /var/lib/field-config/rules.nft; rm /etc/nftables.conf; ln -s /var/lib/field-config/rules.nft /etc/nftables.conf; sync; df -B1 /var/lib/field-config')
if p.returncode:raise SystemExit('state FS setup failed')
rows=[]
for phase in ['50-iops','10-iops','95-percent','100-percent']:
 if phase.endswith('iops'):
  limit=int(phase.split('-')[0]);qmp('block_set_io_throttle',{'device':'field-state','bps':0,'bps_rd':0,'bps_wr':0,'iops':limit,'iops_rd':0,'iops_wr':0})
 else:
  qmp('block_set_io_throttle',{'device':'field-state','bps':0,'bps_rd':0,'bps_wr':0,'iops':0,'iops_rd':0,'iops_wr':0})
  fraction=.95 if phase=='95-percent' else 1.0
  script=f'''import os
p='/var/lib/field-config'
s=os.statvfs(p);target=int(s.f_blocks*s.f_frsize*{fraction});used=(s.f_blocks-s.f_bfree)*s.f_frsize
f=open(p+'/filler','ab',buffering=0)
try:
 while used<target:
  n=f.write(bytes(min(1048576,target-used)));used+=n
 os.fsync(f.fileno())
except OSError as e:print('filler:',repr(e))
print(os.statvfs(p))
'''
  subprocess.run(['/work/field/guest-command','debian','python3 -'],input=script,text=True,check=True)
 guest('client',f'nohup ping -D -i .1 -w 310 198.19.0.1 >/tmp/R11-{phase}-forwarding.txt 2>&1 </dev/null &')
 start=time.monotonic();events=[]
 while time.monotonic()-start<300:
  t=time.monotonic();p=guest('debian',"python3 -c 'import os,time; p=\"/var/lib/field-config/rules.tmp\"; target=\"/var/lib/field-config/rules.nft\"; data=open(target).read().split(\"# field-sequence\")[0]; stamp=str(time.time()); f=open(p,\"w\"); f.write(data+\"# field-sequence \"+stamp+\"\\n\"); f.flush(); os.fsync(f.fileno()); os.replace(p,target); fd=os.open(\"/var/lib/field-config\",os.O_DIRECTORY); os.fsync(fd); os.close(fd); print(\"ACK\",stamp)'",30)
  event={'at':time.time(),'elapsed_s':time.monotonic()-start,'write_s':time.monotonic()-t,'exit':p.returncode,'stdout':p.stdout,'stderr':p.stderr};events.append(event);time.sleep(5)
 (raw/f'R11-debian-{phase}-writes.json').write_text(json.dumps(events,indent=2)+'\n')
 p=guest('client',f'cat /tmp/R11-{phase}-forwarding.txt');(raw/f'R11-debian-{phase}-forwarding.txt').write_text(p.stdout+p.stderr)
 p=guest('debian','df -B1 /var/lib/field-config; dmesg | tail -12');(raw/f'R11-debian-{phase}-state.txt').write_text(p.stdout+p.stderr)
 rows.append({'phase':phase,'duration_s':time.monotonic()-start,'writes':len(events),'failed':sum(r['exit']!=0 for r in events)})
(raw/'R11-debian-storage.summary.json').write_text(json.dumps(rows,indent=2)+'\n')
guest('debian','rm -f /var/lib/field-config/filler; sync')
(raw/'R11-debian-storage.exit').write_text('0\n')
