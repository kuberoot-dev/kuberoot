#!/usr/bin/env python3
import json,socket,struct,time
end=time.monotonic()+300;s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.settimeout(.5)
qname=b'\x0afield-sink\x03lab\x00';out=open('/tmp/R4-dns-load.jsonl','w',buffering=1);i=0
while time.monotonic()<end:
 start=time.monotonic();i=(i+1)%65536;query=struct.pack('!HHHHHH',i,0x100,1,0,0,0)+qname+struct.pack('!HH',1,1)
 try:
  s.sendto(query,('192.168.10.1',53));data,_=s.recvfrom(2048);ident,flags,qd,an,ns,ar=struct.unpack('!HHHHHH',data[:12]);ok=ident==i and flags&15==0 and an>0
 except Exception:ok=False
 out.write(json.dumps({'at':time.time(),'elapsed_s':time.monotonic()-start,'ok':ok})+'\n');time.sleep(max(0,.1-(time.monotonic()-start)))
