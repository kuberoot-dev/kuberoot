#!/usr/bin/env python3
"""Measure actual TCP completion and DUT resources; never treat endpoint OOM as DUT limit."""
import base64,json,subprocess,time
from pathlib import Path
raw=Path('/work/field/raw')
def guest(vm,cmd,timeout=90):
 print(time.strftime('%FT%TZ',time.gmtime()),'$ guest-command',vm,cmd[:300],flush=True)
 p=subprocess.run(['/work/field/guest-command',vm,cmd],capture_output=True,text=True,timeout=timeout);print(p.stdout+p.stderr,flush=True);return p
while not (raw/'R6-debian.exit').exists():time.sleep(3)
guest('debian','nft flush ruleset; nft -f /etc/nftables.conf; cat /proc/sys/net/netfilter/nf_conntrack_max; cat /proc/meminfo | head -3')
for vm in ['client','sink']:
 data=base64.b64encode(Path('/work/field/tcp-flows.py').read_bytes()).decode();guest(vm,f'echo {data} | base64 -d >/tmp/tcp-flows.py')
guest('sink','nohup python3 /tmp/tcp-flows.py server --seconds 2400 >/tmp/R5-server.json 2>/tmp/R5-server.err </dev/null &')
for count,rate in [(1000,1000),(10000,1000),(100000,10000),(500000,10000)]:
 name=f'R5-debian-{count}-{rate}';start=time.monotonic()
 command=f'python3 /tmp/tcp-flows.py client --count {count} --rate {rate} --seconds 120 --hold 30'
 proc=subprocess.Popen(['/work/field/guest-command','client',command],stdout=(raw/(name+'.jsonl')).open('w'),stderr=(raw/(name+'.err')).open('w'))
 with (raw/(name+'-resources.jsonl')).open('w') as f:
  while proc.poll() is None and time.monotonic()-start<180:
   t=time.monotonic();p=guest('debian','cat /proc/sys/net/netfilter/nf_conntrack_count; head -3 /proc/meminfo; cat /proc/net/stat/nf_conntrack',20)
   f.write(json.dumps({'at':time.time(),'ssh_latency_s':time.monotonic()-t,'exit':p.returncode,'stdout':p.stdout,'stderr':p.stderr})+'\n');f.flush();time.sleep(1)
 if proc.poll() is None:proc.terminate()
 for vm in ['client','sink','debian']:
  p=guest(vm,'dmesg | tail -25; cat /proc/meminfo | head -3')
  (raw/(name+'-'+vm+'-health.txt')).write_text(p.stdout+p.stderr)
 time.sleep(10)
(raw/'R5-debian.exit').write_text('0\n')
