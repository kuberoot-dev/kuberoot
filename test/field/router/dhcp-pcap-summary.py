#!/usr/bin/env python3
"""Derive DHCP DORA latency and lease identities from actual packets."""
import argparse,json,math
from pathlib import Path
from scapy.all import PcapReader,BOOTP,DHCP
p=argparse.ArgumentParser();p.add_argument('pcap');p.add_argument('--output',required=True);a=p.parse_args()
first_discover=None;last_ack=None;starts={};ids={};latencies=[];leases={};acks=0;offers=0;discovers=0;address_ids={}
with PcapReader(a.pcap) as packets:
 for packet in packets:
  if BOOTP not in packet or DHCP not in packet:continue
  b=packet[BOOTP];opts={x[0]:x[1] for x in packet[DHCP].options if isinstance(x,tuple) and len(x)>1}
  typ=opts.get('message-type');key=(b.xid,bytes(b.chaddr[:6]));stamp=float(packet.time)
  if typ==1:
   first_discover=stamp if first_discover is None else min(first_discover,stamp);discovers+=1;starts.setdefault(key,stamp);ids[key]=opts.get('client_id',key[1]).hex()
  if typ==2:offers+=1
  if typ==5:
   last_ack=stamp;acks+=1;identity=ids.get(key,key[1].hex());ip=b.yiaddr
   if key in starts:latencies.append(stamp-starts[key])
   leases[identity]={'address':ip,'mac':':'.join(f'{x:02x}' for x in key[1]),'client_id':identity}
   address_ids.setdefault(ip,set()).add(identity)
latencies.sort()
def percentile(p):return latencies[max(0,math.ceil(len(latencies)*p)-1)] if latencies else None
out={'first_discover_at':first_discover,'last_ack_at':last_ack,'last_ack_after_first_discover_s':last_ack-first_discover if last_ack and first_discover else None,'discover_packets':discovers,'offer_packets':offers,'ack_packets':acks,'distinct_acknowledged_clients':len(leases),'distinct_leased_addresses':len(address_ids),'addresses_with_multiple_client_ids':{k:sorted(v) for k,v in address_ids.items() if len(v)>1},'dora_samples':len(latencies),'dora_p50_s':percentile(.5),'dora_p99_s':percentile(.99),'dora_max_s':max(latencies) if latencies else None,'leases':leases}
Path(a.output).write_text(json.dumps(out,indent=2)+'\n');print(json.dumps({k:v for k,v in out.items() if k!='leases'},indent=2))
