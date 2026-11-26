#!/usr/bin/env python3
"""Actual UDP request/reply probes; distinct allowed/denied destination ports."""
import argparse,json,socket,threading,time
p=argparse.ArgumentParser();p.add_argument('mode',choices=['server','client']);p.add_argument('--seconds',type=float,default=60);p.add_argument('--target',default='198.19.0.1');p.add_argument('--output',default='/tmp/field-probes.jsonl');a=p.parse_args()
stop=time.monotonic()+a.seconds
if a.mode=='server':
 def serve(port):
  s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.bind(('0.0.0.0',port));s.settimeout(.5)
  while time.monotonic()<stop:
   try:data,addr=s.recvfrom(65536);s.sendto(data,addr)
   except socket.timeout:pass
 threads=[threading.Thread(target=serve,args=(p,)) for p in (9000,9001)]
 for t in threads:t.start()
 for t in threads:t.join()
else:
 out=open(a.output,'w',buffering=1);lock=threading.Lock()
 def probe(port):
  s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.setblocking(False);pending={};seq=0;next_send=time.monotonic()
  while time.monotonic()<stop:
   now=time.monotonic()
   if now>=next_send:
    payload=str(seq).encode();s.sendto(payload,(a.target,port));pending[seq]=now;seq+=1;next_send+=.01
   try:
    data,_=s.recvfrom(65536);n=int(data);sent=pending.pop(n,None)
    if sent is not None:
     with lock:out.write(json.dumps({'wall_time':time.time(),'port':port,'seq':n,'rtt_s':time.monotonic()-sent})+'\n')
   except BlockingIOError:pass
   time.sleep(.0005)
  with lock:out.write(json.dumps({'port':port,'sent':seq,'unanswered':len(pending),'summary':True})+'\n')
 threads=[threading.Thread(target=probe,args=(p,)) for p in (9000,9001)]
 for t in threads:t.start()
 for t in threads:t.join()
