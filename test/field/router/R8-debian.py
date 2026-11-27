#!/usr/bin/env python3
import json,subprocess,time
from pathlib import Path
raw=Path('/work/field/raw')
def guest(vm,cmd,timeout=60):
 print(time.strftime('%FT%TZ',time.gmtime()),'$ guest-command',vm,cmd,flush=True)
 p=subprocess.run(['/work/field/guest-command',vm,cmd],capture_output=True,text=True,timeout=timeout);print(p.stdout+p.stderr,flush=True);return p
def put(vm,path,data):subprocess.run(['/work/field/guest-command',vm,'cat >'+path],input=data,text=True,check=True)
while not (raw/'R1-debian.exit').exists():time.sleep(3)
observer='log stderr all; router id 192.168.10.2; protocol device {}\nprotocol bgp dut { local 192.168.10.2 as 65003; neighbor 192.168.10.1 as 65000; hold time 9; keepalive time 3; ipv4 { import all; export none; }; }\n'
put('client','/tmp/R8-observer.conf',observer);guest('client','systemctl stop bird; bird -c /tmp/R8-observer.conf -s /tmp/R8-observer.ctl -P /tmp/R8-observer.pid; nohup ping -i .1 -w 300 198.19.0.1 >/tmp/R8-forwarding.txt 2>&1 </dev/null &')
config='''frr defaults traditional
hostname field-debian
service integrated-vtysh-config
ip prefix-list CUSTOMER seq 10 permit 203.0.113.0/24
ip prefix-list LOCAL seq 10 permit 192.0.2.0/24
ip prefix-list OBSERVER seq 10 permit 192.0.2.0/24
ip prefix-list OBSERVER seq 20 permit 203.0.113.0/24
router bgp 65000
 bgp router-id 198.18.0.1
 no bgp ebgp-requires-policy
 no bgp network import-check
 neighbor 198.18.0.2 remote-as 65001
 neighbor 198.18.0.2 timers 3 9
 neighbor 198.18.20.2 remote-as 65002
 neighbor 198.18.20.2 timers 3 9
 neighbor 192.168.10.2 remote-as 65003
 neighbor 192.168.10.2 timers 3 9
 address-family ipv4 unicast
  network 192.0.2.0/24
  neighbor 198.18.0.2 prefix-list CUSTOMER in
  neighbor 198.18.0.2 prefix-list LOCAL out
  neighbor 198.18.20.2 prefix-list LOCAL out
  neighbor 192.168.10.2 prefix-list OBSERVER out
 exit-address-family
'''
put('debian','/etc/frr/frr.conf',config);guest('debian','systemctl restart frr')
upstream='log stderr all; router id 198.18.0.2; protocol device {}\nprotocol static injected { ipv4; route 203.0.113.0/24 blackhole; route 203.0.113.128/25 blackhole; route 0.0.0.0/0 blackhole; route 198.51.100.0/24 blackhole; }\nprotocol bgp dut4 { local 198.18.0.2 as 65001; neighbor 198.18.0.1 as 65000; hold time 9; keepalive time 3; ipv4 { import all; export where source = RTS_STATIC; }; }\n'
put('gateway','/tmp/upstream-1.conf',upstream);guest('gateway','birdc -s /run/field-bird/1.ctl configure');time.sleep(15)
for vm,cmd,name in [('client','birdc -s /tmp/R8-observer.ctl show route all','observer'),('debian',"vtysh -c 'show bgp ipv4 unicast json' -c 'show bgp ipv4 unicast neighbors 198.18.20.2 advertised-routes json'",'rib-and-transit-export')]:
 p=guest(vm,cmd);(raw/f'R8-debian-{name}.txt').write_text(p.stdout+p.stderr)
start=time.monotonic();guest('debian',"vtysh -c 'configure terminal' -c 'no ip prefix-list OBSERVER seq 20' -c 'end' -c 'clear bgp 192.168.10.2 soft out'")
rows=[]
while time.monotonic()-start<30:
 p=guest('client','birdc -s /tmp/R8-observer.ctl show route all');rows.append({'elapsed_s':time.monotonic()-start,'text':p.stdout,'exit':p.returncode})
 if p.returncode==0 and '203.0.113.0/24' not in p.stdout:break
 time.sleep(.5)
(raw/'R8-debian-export-edit.json').write_text(json.dumps(rows,indent=2)+'\n')
# Independently exercise max-prefix 1000 + one, allowing all generated /32s.
guest('debian',"vtysh -c 'configure terminal' -c 'ip prefix-list SCALE seq 10 permit 10.64.0.0/16 le 32' -c 'router bgp 65000' -c 'address-family ipv4 unicast' -c 'no neighbor 198.18.0.2 prefix-list CUSTOMER in' -c 'neighbor 198.18.0.2 prefix-list SCALE in' -c 'neighbor 198.18.0.2 maximum-prefix 1000' -c 'end' -c 'clear bgp 198.18.0.2 soft in'")
subprocess.run(['python3','/work/field/bgp-prefix-generator.py','/work/field/R8-max.conf','--ipv4','1001'],check=True)
put('gateway','/tmp/upstream-1.conf',Path('/work/field/R8-max.conf').read_text());start=time.monotonic();guest('gateway','birdc -s /run/field-bird/1.ctl configure');rows=[]
while time.monotonic()-start<15:
 p=guest('debian',"vtysh -c 'show bgp neighbors 198.18.0.2 json'");rows.append({'elapsed_s':time.monotonic()-start,'text':p.stdout,'exit':p.returncode});time.sleep(.5)
(raw/'R8-debian-max-prefix.json').write_text(json.dumps(rows,indent=2)+'\n')
(raw/'R8-debian.exit').write_text('0\n')
