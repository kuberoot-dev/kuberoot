#!/usr/bin/env python3
"""R-B2 measurements, never labels a fabric-limited result line rate."""
import argparse,json,os,subprocess,time
from pathlib import Path
p=argparse.ArgumentParser();p.add_argument('product');p.add_argument('profile');p.add_argument('--direct',action='store_true');p.add_argument('--mode',choices=['tcp','udp64','both'],default='both');p.add_argument('--seconds',type=int,default=60);p.add_argument('--repeats',type=int,default=5);a=p.parse_args()
base=Path('/work/field/raw');out=base/f'R-B2-{a.product}-{a.profile}.jsonl'
if out.exists():raise SystemExit('Existing run; preserve and use a distinct profile')
def cpu(vm):
 pid=(Path('/work/vms')/vm/'pid').read_text().strip();v=Path(f'/proc/{pid}/stat').read_text().rsplit(')',1)[1].split()
 return (int(v[11])+int(v[12]))/os.sysconf('SC_CLK_TCK')
for mode in (('tcp','udp64') if a.mode=='both' else (a.mode,)):
 for reverse in (False,True):
  for repeat in range(a.repeats):
   target='198.18.33.2' if a.direct else '198.19.0.1'
   command=f'iperf3 -J -c {target} -t {a.seconds} -O 5 -P 8'
   if reverse:command+=' -R'
   if mode=='udp64':command+=' -u -l 64 -b 10M'
   watched=['client','sink']+([] if a.direct else ['gateway'])+([] if a.direct else [a.product])
   before={v:cpu(v) for v in watched};pod_before=Path('/sys/fs/cgroup/cpu.stat').read_text();start=time.monotonic()
   proc=subprocess.run(['/work/field/guest-command','client',command],capture_output=True,text=True,timeout=a.seconds+60)
   elapsed=time.monotonic()-start;after={v:cpu(v) for v in watched}
   data=json.loads(proc.stdout) if proc.stdout.strip() else None
   row={'test_id':'R-B2','product':a.product,'profile':a.profile,'mode':mode,'reverse':reverse,'repeat':repeat+1,'wall_time':time.time(),'elapsed_s':elapsed,'command':command,'exit':proc.returncode,'stderr':proc.stderr,'iperf':data,'cpu_core_seconds':{v:after[v]-before[v] for v in watched},'units':'relative QEMU socket userspace-fabric comparison, not wire line rate','headroom_validated':False,'pod_cpu_stat_before':pod_before,'pod_cpu_stat_after':Path('/sys/fs/cgroup/cpu.stat').read_text()}
   with out.open('a') as f:f.write(json.dumps(row)+'\n')
   print(time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime()), command, flush=True)
   print(json.dumps({'product':a.product,'profile':a.profile,'mode':mode,'reverse':reverse,'repeat':repeat+1,'exit':proc.returncode,'elapsed_s':elapsed,'receiver':data.get('end',{}).get('sum_received',data.get('end',{}).get('sum',{})) if data else None,'cpu_core_seconds':row['cpu_core_seconds']}),flush=True)
   if proc.returncode or not data or 'error' in data:raise SystemExit('failed probe; preserve partial run')
