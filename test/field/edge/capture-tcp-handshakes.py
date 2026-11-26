#!/usr/bin/env python3
"""Read-only gateway packet metadata for W TCP handshakes; no payload captured."""
import argparse,datetime,json,pathlib,socket,struct,time
p=argparse.ArgumentParser();p.add_argument('--seconds',type=int,default=1200);p.add_argument('--output',type=pathlib.Path,required=True);a=p.parse_args()
assert socket.gethostname()=='edge-gateway','Capture only inside the dedicated gateway VM'
s=socket.socket(socket.AF_PACKET,socket.SOCK_RAW,socket.htons(0x0800));s.bind(('lan0',0));s.settimeout(1)
end=time.monotonic()+a.seconds;count=0
with a.output.open('x') as f:
 while time.monotonic()<end:
  try:packet,address=s.recvfrom(65536)
  except socket.timeout:f.flush();continue
  if len(packet)<54 or packet[12:14]!=b'\x08\x00' or packet[23]!=6:continue
  ihl=(packet[14]&15)*4;offset=14+ihl
  if len(packet)<offset+20:continue
  source=socket.inet_ntoa(packet[26:30]);target=socket.inet_ntoa(packet[30:34])
  if '10.78.0.11' not in [source,target]:continue
  sport,dport,seq,ack=struct.unpack('!HHII',packet[offset:offset+12]);flags=packet[offset+13]
  if not ({sport,dport}&{30080,30081}) or not flags&0x06:continue
  row={'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'source':source,'destination':target,'source_port':sport,'destination_port':dport,'sequence':seq,'acknowledgement':ack,'flags':flags,'packet_type':address[2]}
  f.write(json.dumps(row)+'\n');count+=1
  if count%100==0:f.flush()
print(json.dumps({'handshake_metadata_packets':count,'payload_captured':False}))
