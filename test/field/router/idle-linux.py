#!/usr/bin/env python3
"""Observe a real Linux guest hour. No shortened window is called an hour."""
import argparse,json,os,subprocess,time,statistics
from pathlib import Path
p=argparse.ArgumentParser();p.add_argument('vm',choices=['debian','openwrt','vyos']);p.add_argument('--seconds',type=int,default=3600);p.add_argument('--settle',type=int,default=600);a=p.parse_args()
base=Path('/work/field/raw');base.mkdir(exist_ok=True)
out=base/f'R-B1-{a.vm}-idle.jsonl'
if out.exists():raise SystemExit('Refusing to overwrite an existing run')
print(json.dumps({'phase':'settle','seconds':a.settle,'wall_time':time.time()}),flush=True)
time.sleep(a.settle)
start=time.monotonic();rows=[]
with out.open('x',buffering=1) as f:
 while True:
  r={'vm':a.vm,'wall_time':time.time(),'elapsed_s':time.monotonic()-start}
  cmd='cat /proc/meminfo; cat /proc/sys/kernel/random/boot_id; df -k /; cat /proc/stat | head -1'
  if a.vm=='debian':args=['/work/field/guest-command','debian',cmd]
  else:args=['python3','/work/field/console.py',a.vm,cmd]
  proc=subprocess.run(args,capture_output=True,text=True,timeout=25)
  r['guest_text']=proc.stdout;r['guest_exit']=proc.returncode;r['guest_error']=proc.stderr
  q=subprocess.run(['python3','/work/field/qmp.py',a.vm,'query-blockstats'],capture_output=True,text=True,timeout=15)
  r['blockstats']=json.loads(q.stdout) if q.returncode==0 else {'error':q.stderr}
  pid=(Path('/work/vms')/a.vm/'pid').read_text().strip()
  r['qemu_proc_stat']=Path(f'/proc/{pid}/stat').read_text()
  r['qemu_status']=Path(f'/proc/{pid}/status').read_text()
  r['pod_cpu_stat']=Path('/sys/fs/cgroup/cpu.stat').read_text()
  rows.append(r);f.write(json.dumps(r)+'\n')
  if time.monotonic()-start>=a.seconds:break
  time.sleep(min(30,max(0,a.seconds-(time.monotonic()-start))))
elapsed=rows[-1]['elapsed_s']-rows[0]['elapsed_s']
def writes(row):return {v['device']:v['stats']['wr_bytes'] for v in row['blockstats'] if v.get('device')}
valid=all(r['guest_exit']==0 and isinstance(r['blockstats'],list) for r in rows)
summary={'vm':a.vm,'elapsed_s':elapsed,'samples':len(rows),'observed_full_hour':elapsed>=3599,'valid_samples':valid}
if valid:
 aa,bb=writes(rows[0]),writes(rows[-1]);deltas={k:bb[k]-aa[k] for k in aa}
 summary.update(disk_write_bytes_by_device=deltas,disk_write_bytes=sum(deltas.values()),extrapolated_bytes_per_day=sum(deltas.values())*86400/elapsed)
 summary['valid_counters']=all(v>=0 for v in deltas.values())
 import re
 boots={re.findall(r'(?m)^[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}$',r['guest_text'])[0] for r in rows}
 summary['stable_boot']=len(boots)==1
 mem=[int(re.search(r'(?m)^MemTotal:\s+(\d+)',r['guest_text']).group(1))*1024-int(re.search(r'(?m)^MemAvailable:\s+(\d+)',r['guest_text']).group(1))*1024 for r in rows]
 summary['guest_total_minus_available_median_bytes']=statistics.median(mem)
 summary['guest_total_minus_available_max_bytes']=max(mem)
out.with_suffix('.summary.json').write_text(json.dumps(summary,indent=2)+'\n');print(json.dumps(summary),flush=True)
if not (valid and summary['observed_full_hour'] and summary.get('valid_counters') and summary.get('stable_boot')):raise SystemExit('Incomplete or invalid observed hour; preserve raw')
