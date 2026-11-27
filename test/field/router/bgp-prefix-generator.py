#!/usr/bin/env python3
"""Generate pinned synthetic BIRD routes; never fetch a moving Internet table."""
import argparse,ipaddress,json
from pathlib import Path
p=argparse.ArgumentParser();p.add_argument('output');p.add_argument('--ipv4',type=int,default=10000);p.add_argument('--ipv6',type=int,default=0);p.add_argument('--asn',type=int,default=65001);p.add_argument('--local',default='198.18.0.2');p.add_argument('--peer',default='198.18.0.1');a=p.parse_args()
if not 0<=a.ipv4<=800000 or not 0<=a.ipv6<=200000:raise SystemExit('outside bound')
with open(a.output,'w') as f:
 f.write(f'log stderr all; router id {a.local}; protocol device {{}}\n')
 f.write('protocol static generated4 { ipv4;\n')
 base=int(ipaddress.IPv4Address('10.64.0.0'))
 for i in range(a.ipv4):f.write(f'route {ipaddress.IPv4Address(base+i)}/32 blackhole;\n')
 f.write('}\n')
 if a.ipv6:
  f.write('protocol static generated6 { ipv6;\n');base6=int(ipaddress.IPv6Address('fd42:100::'))
  for i in range(a.ipv6):f.write(f'route {ipaddress.IPv6Address(base6+i)}/128 blackhole;\n')
  f.write('}\n')
 f.write(f'protocol bgp dut4 {{ local {a.local} as {a.asn}; neighbor {a.peer} as 65000; hold time 9; keepalive time 3; ipv4 {{ import none; export all; }}; }}\n')
 # IPv6 peers use a separate family-specific session, matching kuberoot API.
 if a.ipv6:
  side=1 if a.asn==65001 else 2
  f.write(f'protocol bgp dut6 {{ local fd42:{side}::2 as {a.asn}; neighbor fd42:{side}::1 as 65000; hold time 9; keepalive time 3; ipv6 {{ import none; export all; }}; }}\n')
Path(a.output+'.manifest.json').write_text(json.dumps({'synthetic':True,'ipv4':a.ipv4,'ipv6':a.ipv6,'ipv4_base':'10.64.0.0/32','ipv6_base':'fd42:100::/128','seed':0,'asn':a.asn,'overlap':'both upstreams announce the same prefixes'},indent=2)+'\n')
