#!/usr/bin/env python3
"""Completed TCP connections, not SYN packets passed off as established flows."""
import argparse,json,resource,selectors,socket,time
p=argparse.ArgumentParser();p.add_argument('mode',choices=['server','client']);p.add_argument('--target',default='198.19.0.1');p.add_argument('--count',type=int,default=1000);p.add_argument('--rate',type=int,default=1000);p.add_argument('--hold',type=int,default=30);p.add_argument('--seconds',type=int,default=120);a=p.parse_args()
resource.setrlimit(resource.RLIMIT_NOFILE,(min(1048576,resource.getrlimit(resource.RLIMIT_NOFILE)[1]),resource.getrlimit(resource.RLIMIT_NOFILE)[1]))
sel=selectors.DefaultSelector();live=[];start=time.monotonic();errors={};completed=0;attempted=0
if a.mode=='server':
 for port in range(9100,9132):
  s=socket.socket();s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);s.bind(('0.0.0.0',port));s.listen(65535);s.setblocking(False);sel.register(s,selectors.EVENT_READ,'listen')
 while time.monotonic()-start<a.seconds:
  for key,mask in sel.select(.1):
   try:
    if key.data=='listen':
     for _ in range(100):
      try:c,addr=key.fileobj.accept();c.setblocking(False);sel.register(c,selectors.EVENT_READ,'client');completed+=1
      except BlockingIOError:break
    else:
     data=key.fileobj.recv(1024)
     if not data:sel.unregister(key.fileobj);key.fileobj.close()
   except OSError as e:errors[str(e.errno)]=errors.get(str(e.errno),0)+1
 print(json.dumps({'accepted':completed,'errors':errors,'elapsed_s':time.monotonic()-start}))
else:
 next_report=start
 while (attempted<a.count or sel.get_map()) and time.monotonic()-start<a.seconds:
  due=min(a.count,int((time.monotonic()-start)*a.rate)+1)
  while attempted<due:
   attempted+=1;s=socket.socket();s.setblocking(False)
   try:
    # 32 destinations keep a single-source NAT from being a 64k tuple test.
    code=s.connect_ex((a.target,9100+(attempted%32)))
    if code not in (0,115):raise OSError(code,'connect')
    sel.register(s,selectors.EVENT_WRITE)
   except OSError as e:errors[str(e.errno)]=errors.get(str(e.errno),0)+1;s.close()
  for key,_ in sel.select(.001):
   s=key.fileobj;sel.unregister(s);err=s.getsockopt(socket.SOL_SOCKET,socket.SO_ERROR)
   if err:errors[str(err)]=errors.get(str(err),0)+1;s.close()
   else:completed+=1;live.append(s)
  if time.monotonic()>=next_report:
   print(json.dumps({'at':time.time(),'attempted':attempted,'completed':completed,'errors':errors,'elapsed_s':time.monotonic()-start}),flush=True);next_report+=5
 print(json.dumps({'phase':'hold','requested':a.count,'attempted':attempted,'completed':completed,'completion_ratio':completed/a.count,'elapsed_s':time.monotonic()-start,'hold_s':a.hold,'errors':errors}),flush=True)
 time.sleep(a.hold)
 for s in live:s.close()
 print(json.dumps({'phase':'closed','at':time.time(),'completed':completed}),flush=True)
