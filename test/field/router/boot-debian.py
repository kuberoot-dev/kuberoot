#!/usr/bin/env python3
"""Real installed cold boot -> 30 consecutive successful forwarding seconds."""
import argparse,json,subprocess,time
from pathlib import Path
base=Path('/work/field/raw');console=Path('/work/vms/debian/console.log')
parser=argparse.ArgumentParser();parser.add_argument('--trials',type=int,default=10);args=parser.parse_args()
for trial in range(1,args.trials+1):
 out=base/f'R-B1-debian-boot-{trial:02}.json'
 if out.exists():continue
 subprocess.run(['/work/field/guest-command','debian','sync; poweroff'],check=True)
 deadline=time.monotonic()+60
 while Path('/work/vms/debian/pid').exists() and time.monotonic()<deadline:time.sleep(1)
 if Path('/work/vms/debian/pid').exists():raise SystemExit('guest did not power off')
 offset=console.stat().st_size;start=time.monotonic();wall=time.time()
 subprocess.run(['bash','/work/field/fabric-competitor.sh','debian'],check=True)
 first=None;streak=None;samples=[]
 while time.monotonic()-start<240:
  p=subprocess.run(['/work/field/guest-command','client','ping -n -c 1 -W 1 198.19.0.1'],capture_output=True,text=True,timeout=10)
  elapsed=time.monotonic()-start;ok=p.returncode==0;samples.append({'elapsed_s':elapsed,'ok':ok,'output':p.stdout})
  if ok:
   if first is None:first=elapsed
   if streak is None:streak=elapsed
   if elapsed-streak>=30:break
  else:streak=None
  time.sleep(1)
 row={'test_id':'R-B1','product':'debian','trial':trial,'wall_time':wall,'first_forward_s':first,'stable_forward_s':elapsed if streak is not None and elapsed-streak>=30 else None,'samples':samples,'note':'positive NAT probe only; negative filter boot gate is R6, not inferred here'}
 out.write_text(json.dumps(row,indent=2)+'\n')
 with console.open('rb') as f:f.seek(offset);(base/f'R-B1-debian-boot-{trial:02}-serial.txt').write_bytes(f.read())
 print(json.dumps({k:v for k,v in row.items() if k!='samples'}),flush=True)
 if row['stable_forward_s'] is None:raise SystemExit('forwarding did not stabilize')
