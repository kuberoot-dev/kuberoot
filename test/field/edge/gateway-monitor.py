#!/usr/bin/env python3
"""Read only guest gateway conntrack pressure throughout every WAN trial."""
import argparse,datetime,json,pathlib,socket,time
p=argparse.ArgumentParser();p.add_argument('--output',type=pathlib.Path,required=True);p.add_argument('--stop-file',type=pathlib.Path,required=True);p.add_argument('--seconds',type=float,default=4200);a=p.parse_args()
assert socket.gethostname()=='edge-gateway';end=time.monotonic()+a.seconds
with a.output.open('x') as f:
 while time.monotonic()<end and not a.stop_file.exists():
  lines=pathlib.Path('/proc/net/stat/nf_conntrack').read_text().splitlines();keys=lines[0].split();totals={k:sum(int(line.split()[i],16) for line in lines[1:]) for i,k in enumerate(keys) if k in ['drop','early_drop','insert_failed']}
  totals['count']=int(pathlib.Path('/proc/sys/net/netfilter/nf_conntrack_count').read_text());totals['max']=int(pathlib.Path('/proc/sys/net/netfilter/nf_conntrack_max').read_text())
  row={'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'monotonic':time.monotonic(),'conntrack':totals}
  f.write(json.dumps(row)+'\n');f.flush();time.sleep(1)
