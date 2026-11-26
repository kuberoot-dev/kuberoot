#!/usr/bin/env python3
import json,subprocess,time
from pathlib import Path
raw=Path('/work/field/raw')
while not (raw/'R11-debian-hour.exit').exists():time.sleep(3)
def guest(vm,cmd):
 print(time.strftime('%FT%TZ',time.gmtime()),'$ guest-command',vm,cmd,flush=True)
 p=subprocess.run(['/work/field/guest-command',vm,cmd],capture_output=True,text=True,timeout=30);print(p.stdout+p.stderr,flush=True);return p
# Full real reboot check, require boot_id to change; /root survives reboot.
pre=guest('debian','cat /var/lib/misc/dnsmasq.leases').stdout;boot=guest('debian','cat /proc/sys/kernel/random/boot_id').stdout.strip();guest('debian','systemctl reboot');start=time.monotonic();post=''
while time.monotonic()-start<120:
 p=guest('debian','cat /proc/sys/kernel/random/boot_id; cat /var/lib/misc/dnsmasq.leases')
 if p.returncode==0 and p.stdout.splitlines()[0]!=boot:post='\n'.join(p.stdout.splitlines()[1:]);break
 time.sleep(2)
def leases(text):return sorted(tuple(x.split()[1:]) for x in text.splitlines() if len(x.split())==5)
summary={'old_boot_id':boot,'new_boot_id':p.stdout.splitlines()[0] if p.returncode==0 else None,'recovery_s':time.monotonic()-start,'leases_preserved':leases(pre)==leases(post),'before_count':len(leases(pre)),'after_count':len(leases(post))}
(raw/'R4-debian-verified-reboot.json').write_text(json.dumps(summary,indent=2)+'\n')
guest('debian','nft list ruleset >/root/R4-before-isolation.nft; ip link add link enp0s3 name guest20 type vlan id 20; ip addr add 192.168.20.1/24 dev guest20; ip link set guest20 up')
text='''flush ruleset
table inet fieldguest {
 chain forward { type filter hook forward priority filter; policy drop;
 ct state established,related accept
 iifname "guest20" oifname "enp0s3" counter drop
 iifname "guest20" oifname "enp0s2" accept
 iifname "enp0s3" oifname "enp0s2" accept
 }
}
table ip fieldnat {
 chain postrouting { type nat hook postrouting priority srcnat; policy accept;
 oifname "enp0s2" ip saddr {192.168.10.0/24,192.168.20.0/24} masquerade
 }
}
'''
subprocess.run(['/work/field/guest-command','debian','nft -f -'],input=text,text=True,check=True)
guest('client','ip netns add guest20; ip link add link test0 name guest20 type vlan id 20; ip link set guest20 netns guest20; ip netns exec guest20 ip addr add 192.168.20.2/24 dev guest20; ip netns exec guest20 ip link set guest20 up; ip netns exec guest20 ip link set lo up; ip netns exec guest20 ip route add default via 192.168.20.1; ip route replace 192.168.20.0/24 via 192.168.10.1')
rows=[]
for name,target in [('gateway-positive','192.168.20.1'),('wan-positive','198.19.0.1'),('employee-denied','192.168.10.2')]:
 p=guest('client',f'ip netns exec guest20 ping -c 20 -i .1 -W 1 {target}');rows.append({'case':name,'exit':p.returncode,'text':p.stdout+p.stderr})
p=guest('debian','nft list ruleset');rows.append({'case':'kernel-counters','text':p.stdout+p.stderr})
(raw/'R4-debian-guest-isolation.json').write_text(json.dumps(rows,indent=2)+'\n')
guest('client','ip netns del guest20; ip route del 192.168.20.0/24');guest('debian','ip link del guest20; nft flush ruleset; nft -f /root/R4-before-isolation.nft')
(raw/'R4-debian-isolation.exit').write_text('0\n')
