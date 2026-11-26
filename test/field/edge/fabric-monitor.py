#!/usr/bin/env python3
"""Read-only host-network evidence; no sysctl or privileges are requested."""
import argparse,datetime,json,pathlib,time
p=argparse.ArgumentParser();p.add_argument('--output',type=pathlib.Path,required=True);p.add_argument('--seconds',type=float,default=14400);a=p.parse_args();end=time.monotonic()+a.seconds
while time.monotonic()<end:
 lines=pathlib.Path('/proc/net/snmp').read_text().splitlines();counters={}
 for i in range(0,len(lines)-1,2):
  keys=lines[i].split();values=lines[i+1].split()
  if keys[0] in ['Udp:','Tcp:']:counters[keys[0][:-1]]=dict(zip(keys[1:],map(int,values[1:])))
 sockets=[line.split() for line in pathlib.Path('/proc/net/udp').read_text().splitlines()[1:] if line.split()[1].endswith(':4589')]
 row={'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'network_namespace_counters':counters,'multicast_17801_sockets':sockets,'cpu_stat':pathlib.Path('/sys/fs/cgroup/cpu.stat').read_text()}
 with a.output.open('a') as f:f.write(json.dumps(row)+'\n')
 time.sleep(5)
