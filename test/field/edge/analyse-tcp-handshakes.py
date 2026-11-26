#!/usr/bin/env python3
"""Pair captured W SYN/SYN-ACK metadata and correlate actual connect timeouts."""
import argparse,collections,datetime,json,pathlib,time
p=argparse.ArgumentParser();p.add_argument('packets',type=pathlib.Path);p.add_argument('observer',type=pathlib.Path);p.add_argument('--output',type=pathlib.Path,required=True);a=p.parse_args()
def load(path):
 rows=[]
 for line in path.read_text().splitlines():
  try:rows.append(json.loads(line))
  except json.JSONDecodeError:continue # concurrent final append
 return rows
def epoch(r):return datetime.datetime.fromisoformat(r['utc']).timestamp()
packets=load(a.packets);observations=load(a.observer);syns={};acks={}
for r in packets:
 if r['source']=='10.78.0.1' and r['flags']&0x12==0x02:
  key=(r['source_port'],r['destination_port'],r['sequence']);syns.setdefault(key,[]).append(r)
 elif r['destination']=='10.78.0.1' and r['flags']&0x12==0x12:
  key=(r['destination_port'],r['source_port'],(r['acknowledgement']-1)%2**32);acks.setdefault(key,[]).append(r)
end=max(map(epoch,packets));start=min(map(epoch,packets));complete={k:v for k,v in syns.items() if epoch(v[0])<end-3}
missing=[{'source_port':k[0],'destination_port':k[1],'sequence':k[2],'first_syn_utc':v[0]['utc'],'syn_count':len(v)} for k,v in complete.items() if k not in acks]
correlated=[]
for r in observations:
 if r.get('kind') not in ['http','append'] or r.get('ok') or 'utc' not in r or not start<=epoch(r)<=end-3:continue
 port=30080 if r['kind']=='http' else 30081
 matches=[(k,v) for k,v in complete.items() if k[1]==port and abs(epoch(v[0])-epoch(r))<.05]
 correlated.append({'request_utc':r['utc'],'kind':r['kind'],'error':r.get('error'),'matching_syns':[{'source_port':k[0],'syn_count':len(v),'synack_seen':k in acks,'first_synack_seconds':epoch(acks[k][0])-epoch(v[0]) if k in acks else None} for k,v in matches]})
result={'coverage_utc':[packets[0]['utc'],packets[-1]['utc']],'completed_connection_attempts':len(complete),'syn_retransmitted_connections':sum(len(v)>1 for v in complete.values()),'missing_synack_connections':missing,'observer_failures_in_capture':correlated,'scope':'Gateway LAN headers only; no conclusion about which downstream component dropped a packet.'}
a.output.write_text(json.dumps(result,indent=2));print(json.dumps(result,indent=2))
