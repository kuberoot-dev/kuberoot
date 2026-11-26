#!/usr/bin/env python3
import json,subprocess,time
from pathlib import Path
raw=Path('/work/field/raw')
while not (raw/'R3-debian.exit').exists():time.sleep(3)
def qmp():return json.loads(subprocess.check_output(['python3','/work/field/qmp.py','debian','query-blockstats']))
def guest(cmd):return subprocess.run(['/work/field/guest-command','debian',cmd],capture_output=True,text=True)
print(time.strftime('%FT%TZ',time.gmtime()),'native nft configuration write load: offered 10/s, one full observed hour',flush=True)
subprocess.run(['/work/field/guest-command','debian','cat >/root/R11-write-load.py'],input=Path('/work/field/R11-write-load.py').read_bytes(),check=True)
before=qmp();start=time.monotonic();p=subprocess.Popen(['/work/field/guest-command','debian','python3 /root/R11-write-load.py'],stdout=(raw/'R11-debian-write-load.jsonl').open('w'),stderr=(raw/'R11-debian-write-load.err').open('w'))
with (raw/'R11-debian-hour-observer.jsonl').open('w') as f:
 while p.poll() is None:
  snap={'at':time.time(),'elapsed_s':time.monotonic()-start,'blockstats':qmp(),'pod_cpu_stat':Path('/sys/fs/cgroup/cpu.stat').read_text()};f.write(json.dumps(snap)+'\n');f.flush();print(json.dumps({'at':snap['at'],'elapsed_s':snap['elapsed_s']}),flush=True);time.sleep(30)
after=qmp();elapsed=time.monotonic()-start
def writes(rows):return {r['device']:r['stats']['wr_bytes'] for r in rows if r.get('device')}
a,b=writes(before),writes(after);delta={k:b[k]-a[k] for k in a}
summary={'exit':p.returncode,'observer_elapsed_s':elapsed,'write_bytes_by_device':delta,'bytes_per_day_extrapolated':sum(delta.values())*86400/elapsed,'scope':'native nft table updates; persisted in /var/lib/field-config/load.nft; not a transaction API'}
(raw/'R11-debian-hour.summary.json').write_text(json.dumps(summary,indent=2)+'\n');print(json.dumps(summary),flush=True)
if p.returncode==0:(raw/'R11-debian-hour.exit').write_text('0\n')
