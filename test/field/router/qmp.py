#!/usr/bin/env python3
"""Execute a real QMP command against one explicitly selected field VM."""
import json,socket,sys
from pathlib import Path
name,cmd=sys.argv[1:3]
if name not in ('r1','r2','openwrt','vyos','debian','client','gateway','sink'): raise SystemExit('unknown field VM')
s=socket.socket(socket.AF_UNIX);s.settimeout(10)
s.connect(str(Path('/work/vms')/name/'qmp.sock'))
f=s.makefile('rwb',buffering=0)
json.loads(f.readline())
def run(command,args=None):
 f.write((json.dumps({'execute':command,'arguments':args or {}})+'\n').encode())
 while True:
  reply=json.loads(f.readline())
  if 'error' in reply: raise RuntimeError(reply['error'])
  if 'return' in reply:return reply['return']
run('qmp_capabilities')
print(json.dumps(run(cmd,json.loads(sys.argv[3]) if len(sys.argv)>3 else None),indent=2))
