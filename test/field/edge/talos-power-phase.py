#!/usr/bin/env python3
"""Verified native Talos download/write/EFI selection/fallback-pending cuts."""
import json,os,pathlib,subprocess,sys,time
from qmp import command
import talos_trial as t
phase=sys.argv[1];assert phase in ['download','inactive-write','boot-selection','probation'];d=pathlib.Path(os.environ['VM_DIR']);delay=float(os.environ['FIELD_PHASE_DELAY']);t.configure(d);before=t.boot_id();entry=t.boot_default();t.counter(reset=True)
if phase=='download':subprocess.run(['bash',str(t.S/'wan-impairment.sh'),'slow'],check=True)
if phase=='inactive-write':command(str(d/'qmp.sock'),'qom-set',{'path':'/objects/field-throttle','property':'limits','value':{'bps-total':1024**2}})
p=t.upgrade(d,no_reboot=phase!='probation')
def observed():
 text=(d/'talos-upgrade-progress.txt').read_text(errors='replace')
 if phase=='download':
  count=t.counter();return count>65536 and 'copying ' not in text and 'upgrade completed' not in text and p.poll() is None,{'forwarded_ipv4_bytes':count,'native_progress_tail':text[-2500:]}
 if phase=='inactive-write':
  copying='copying ' in text and '.efi' in text
  return copying and 'updating EFI variables' not in text and p.poll() is None,{'native_progress_tail':text[-3000:]}
 if phase=='boot-selection':
  current=t.boot_default();return current!=entry,{'previous_loader_default':entry,'current_loader_default':current,'native_no_reboot':True}
 current=t.boot_id()
 # 0x06 is the real Upgrade META tag. It disappears on Running+Ready.
 tag=t.call('get','meta','0x06','-o','json')
 return current!=before and bool(tag.strip()),{'boot_id':current,'upgrade_fallback_meta':json.loads(tag),'note':'Talos fallback-pending phase, not kuberoot five-minute probation'}
try:
 end=time.monotonic()+1200
 while time.monotonic()<end:
  try:
   ok,evidence=observed()
   if ok:
    first=time.time();time.sleep(delay);ok,evidence=observed()
    if not ok:raise RuntimeError('requested phase ended before seeded offset')
    command(str(d/'qmp.sock'),'stop');pause=time.time();assert command(str(d/'qmp.sock'),'query-status')['status']=='paused'
    value={'phase':phase,'pause_epoch':pause,'first_observation_epoch':first,'seeded_offset_seconds':delay,'actual_offset_seconds':pause-first,'before_pause':evidence,'source_reference':'siderolabs/talos@55021d7c274f6bc207bf18efa2a684a31a35cd9f; native lifecycle upgrade API'}
    (d/'phase-evidence.json').write_text(json.dumps(value,indent=2));print(json.dumps(value),flush=True);break
  except (RuntimeError,subprocess.TimeoutExpired) as exc:
   if 'phase ended' in str(exc):raise
  time.sleep(.1)
 else:raise RuntimeError('native requested Talos upgrade phase not observed')
finally:
 if phase=='download':subprocess.run(['bash',str(t.S/'wan-impairment.sh'),'clean'],check=True)
 if p.poll() is None:p.terminate();p.wait(timeout=10)
