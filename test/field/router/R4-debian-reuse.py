#!/usr/bin/env python3
"""Release twenty actual leases, retry, then reboot and compare retained identities."""
import json,subprocess,time
from pathlib import Path
base=Path('/work/field/raw')
def guest(vm,cmd,timeout=60):
 print(time.strftime('%FT%TZ',time.gmtime()),'$ guest-command',vm,cmd,flush=True)
 p=subprocess.run(['/work/field/guest-command',vm,cmd],capture_output=True,text=True,timeout=timeout);print(p.stdout+p.stderr,flush=True);return p
before=guest('debian','cat /var/lib/misc/dnsmasq.leases').stdout
leases=[line.split() for line in before.splitlines() if len(line.split())==5][:20]
script='from scapy.all import Ether,IP,UDP,BOOTP,DHCP,sendp\nimport time\n'
for _,mac,ip,host,cid in leases:
 opts=[('message-type','release'),('server_id','192.168.10.1')]
 if cid!='*':opts.append(('client_id',bytes.fromhex(cid.replace(':',''))))
 opts.append('end')
 script+=f'sendp(Ether(src={mac!r},dst="ff:ff:ff:ff:ff:ff")/IP(src={ip!r},dst="192.168.10.1")/UDP(sport=68,dport=67)/BOOTP(op=1,ciaddr={ip!r},chaddr=bytes.fromhex({mac.replace(":", "")!r}))/DHCP(options={opts!r}),iface="test0",verbose=False)\n'
import base64
encoded=base64.b64encode(script.encode()).decode()
guest('client',f'echo {encoded} | base64 -d > /tmp/field-release.py; python3 /tmp/field-release.py')
after=guest('debian','cat /var/lib/misc/dnsmasq.leases').stdout
(base/'R4-debian-release-before.txt').write_text(before);(base/'R4-debian-release-after.txt').write_text(after)
guest('client','tcpdump -U -n -i test0 -s 0 -w /tmp/R4-reuse.pcap "udp port 67 or udp port 68" >/tmp/R4-reuse-capture.log 2>&1 & echo $! >/tmp/R4-reuse.pid')
start=time.monotonic();guest('client','timeout --signal=INT --kill-after=2s 10s perfdhcp -4 -B -l test0 -b mac=02:43:00:00:00:00 -s 43 --scenario avalanche -R 20 -p 8 -x aelt',20)
guest('client','kill -INT $(cat /tmp/R4-reuse.pid); sleep 1; python3 /tmp/dhcp-pcap-summary.py /tmp/R4-reuse.pcap --output /tmp/R4-reuse.summary.json')
for ext in ['pcap','summary.json']:
 data=subprocess.check_output(['/work/field/guest-command','client',f'cat /tmp/R4-reuse.{ext}']);(base/f'R4-debian-reuse.{ext}').write_bytes(data)
pre=guest('debian','cat /var/lib/misc/dnsmasq.leases').stdout
old_boot=guest('debian','cat /proc/sys/kernel/random/boot_id').stdout.strip()
guest('debian','systemctl reboot');start=time.monotonic();post=None
while time.monotonic()-start<100:
 p=guest('debian','cat /proc/sys/kernel/random/boot_id; cat /var/lib/misc/dnsmasq.leases')
 if p.returncode==0 and p.stdout.splitlines()[0]!=old_boot:post='\n'.join(p.stdout.splitlines()[1:]);break
 time.sleep(2)
def identities(s):return sorted(tuple(line.split()[1:]) for line in (s or '').splitlines() if len(line.split())==5)
summary={'released':len(leases),'leases_before_release':len(identities(before)),'leases_after_release':len(identities(after)),'reboot_ssh_recovery_s':time.monotonic()-start,'leases_before_reboot':len(identities(pre)),'leases_after_reboot':len(identities(post)),'identities_preserved':identities(pre)==identities(post)}
(base/'R4-debian-reuse-reboot.summary.json').write_text(json.dumps(summary,indent=2)+'\n');print(json.dumps(summary),flush=True)
