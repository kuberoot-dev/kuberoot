#!/usr/bin/env python3
"""Run a real guest shell command over serial, preserving exit status."""
import argparse,re,select,socket,sys,time,uuid
p=argparse.ArgumentParser();p.add_argument('vm',choices=['openwrt','vyos','debian']);p.add_argument('command');p.add_argument('--timeout',type=int,default=120);a=p.parse_args()
s=socket.socket(socket.AF_UNIX);s.settimeout(10);s.connect('/work/vms/'+a.vm+'/serial.sock');s.setblocking(False)
def until(pattern,seconds):
 end=time.monotonic()+seconds;data=b''
 while time.monotonic()<end:
  if select.select([s],[],[],.1)[0]:
   chunk=s.recv(65536)
   if not chunk:raise RuntimeError('serial closed')
   data+=chunk
   if re.search(pattern,data):return data
 raise RuntimeError('serial timeout: '+data.decode(errors='replace'))
s.sendall(b'\n');data=until(rb'(login:|Password:|[#\$] |activate this console)',20)
if b'activate this console' in data:s.sendall(b'\n');data=until(rb'[#\$] ',20)
if b'login:' in data:
 s.sendall(b'vyos\n' if a.vm=='vyos' else b'root\n');data=until(rb'(Password:|[#\$] )',20)
if b'Password:' in data:
 if a.vm!='vyos':raise RuntimeError('unexpected password request')
 s.sendall(b'vyos\n');until(rb'[#\$] ',20)
mark='FIELD_'+uuid.uuid4().hex
s.sendall((a.command+"; printf '\\n"+mark+":%s\\n' \"$?\"\n").encode())
data=until(rb'\r?\n'+mark.encode()+rb':([0-9]+)\r?\n',a.timeout)
text=data.decode(errors='replace').replace('\r','')
print(text,end='');sys.exit(int(re.search('\n'+mark+':([0-9]+)',text).group(1)))
