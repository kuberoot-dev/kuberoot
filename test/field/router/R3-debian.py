#!/usr/bin/env python3
import json,subprocess,time
from pathlib import Path
raw=Path('/work/field/raw')
while not (raw/'R7-debian-full8.exit').exists():time.sleep(3)
def guest(cmd):
 print(time.strftime('%FT%TZ',time.gmtime()),'$ guest-command debian',cmd,flush=True)
 p=subprocess.run(['/work/field/guest-command','debian',cmd],capture_output=True,text=True,timeout=20);print(p.stdout+p.stderr,flush=True);return p
p=guest("nft add table inet fielddrift; nft 'add chain inet fielddrift forward { type filter hook forward priority 10; policy accept; }'; nft add rule inet fielddrift forward udp dport 9001 counter drop comment drift-sentinel; nft -a list table inet fielddrift")
(raw/'R3-debian-drift-before.txt').write_text(p.stdout+p.stderr)
guest("nft -a list chain inet fielddrift forward | sed -n '/comment \"drift-sentinel\"/s/.*# handle //p' | xargs -I HANDLE nft delete rule inet fielddrift forward handle HANDLE")
start=time.monotonic();rows=[]
for _ in range(8):
 p=guest('nft -a list table inet fielddrift');rows.append({'at':time.time(),'elapsed_s':time.monotonic()-start,'exit':p.returncode,'text':p.stdout});time.sleep(5)
(raw/'R3-debian-drift.json').write_text(json.dumps(rows,indent=2)+'\n')
guest('nft delete table inet fielddrift; nft add table inet fieldrace')
procs=[]
for name,port in [('writer-a',9010),('writer-b',9020)]:
 text=f'delete table inet fieldrace\ntable inet fieldrace {{\n chain forward {{ type filter hook forward priority 10; policy accept;\n udp dport {port} drop comment "{name}"\n }}\n}}\n'
 p=subprocess.Popen(['/work/field/guest-command','debian','nft -f -'],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True);p.stdin.write(text);p.stdin.close();procs.append((name,p))
rows=[]
for name,p in procs:
 p.wait();rows.append({'writer':name,'exit':p.returncode,'stdout':p.stdout.read(),'stderr':p.stderr.read()})
p=guest('nft list table inet fieldrace');rows.append({'final':p.stdout,'exit':p.returncode});(raw/'R3-debian-writers.json').write_text(json.dumps(rows,indent=2)+'\n')
guest('nft delete table inet fieldrace');(raw/'R3-debian.exit').write_text('0\n')
