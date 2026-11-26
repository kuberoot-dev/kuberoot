#!/usr/bin/env python3
import json,subprocess,time
from pathlib import Path
raw=Path('/work/field/raw')
def guest(vm,cmd,timeout=60):
 print(time.strftime('%FT%TZ',time.gmtime()),'$ guest-command',vm,cmd,flush=True)
 p=subprocess.run(['/work/field/guest-command',vm,cmd],capture_output=True,text=True,timeout=timeout);print(p.stdout+p.stderr,flush=True);return p
def put(vm,path,data):subprocess.run(['/work/field/guest-command',vm,'cat >'+path],input=data,text=True,check=True)
while not (raw/'R8-debian.exit').exists():time.sleep(3)
config='frr defaults traditional\nhostname field-debian\nservice integrated-vtysh-config\nrouter bgp 65000\n bgp router-id 198.18.0.1\n no bgp ebgp-requires-policy\n'
for peer,asn in [('198.18.0.2',65001),('198.18.20.2',65002)]:config+=f' neighbor {peer} remote-as {asn}\n neighbor {peer} timers 3 9\n'
put('debian','/etc/frr/frr.conf',config);guest('debian','systemctl restart frr')
for side in [1,2]:
 path=f'/work/field/R9-upstream-{side}.conf';subprocess.run(['python3','/work/field/bgp-prefix-generator.py',path,'--ipv4','1000','--asn',str(65000+side),'--local','198.18.0.2' if side==1 else '198.18.20.2','--peer','198.18.0.1' if side==1 else '198.18.20.1'],check=True)
 put('gateway',f'/tmp/upstream-{side}.conf',Path(path).read_text());guest('gateway',f'birdc -s /run/field-bird/{side}.ctl configure')
guest('gateway','ip netns exec upstream2 ip route replace default via 198.18.20.1');time.sleep(15)
guest('client','nohup timeout 480 fping -D -l -p 100 10.64.0.0 >/tmp/R9-probes.txt 2>&1 </dev/null &')
events=[]
def event(phase,cmd):
 t=time.time();p=guest('debian',cmd);events.append({'at':t,'phase':phase,'command':cmd,'exit':p.returncode,'stdout':p.stdout,'stderr':p.stderr});(raw/'R9-debian-events.json').write_text(json.dumps(events,indent=2)+'\n')
# Session reset exercises hard protocol failure; not mislabeled carrier-down.
event('hard-session-reset',"vtysh -c 'clear bgp 198.18.0.2'");time.sleep(20)
# Carrier stays UP: drop all packets on U1 native WAN, U2 VLAN stays usable.
event('silent-blackhole-start',"nft add table inet fieldblackhole; nft 'add chain inet fieldblackhole input { type filter hook input priority -200; policy accept; }'; nft add rule inet fieldblackhole input iifname enp0s2 drop; ip -br link show enp0s2")
time.sleep(20);event('silent-blackhole-end','nft delete table inet fieldblackhole');time.sleep(20)
for i in range(60):
 cmd="vtysh -c 'configure terminal' -c 'router bgp 65000' -c 'neighbor 198.18.0.2 shutdown'" if i%2==0 else "vtysh -c 'configure terminal' -c 'router bgp 65000' -c 'no neighbor 198.18.0.2 shutdown'"
 t=time.monotonic();event(f'flap-{i}',cmd);time.sleep(max(0,5-(time.monotonic()-t)))
time.sleep(30)
event('bgpd-crash-with-zebra-alive',"ip route show proto bgp | wc -l; kill -KILL $(cat /run/frr/bgpd.pid); sleep 1; ip route show proto bgp | wc -l")
time.sleep(20)
event('restart-with-fib-flush','ip route flush proto bgp; systemctl restart frr');time.sleep(20)
p=guest('client','cat /tmp/R9-probes.txt');(raw/'R9-debian-probes.txt').write_text(p.stdout+p.stderr)
p=guest('debian',"vtysh -c 'show bgp ipv4 unicast summary json'; ip route show proto bgp | wc -l");(raw/'R9-debian-final.txt').write_text(p.stdout+p.stderr)
(raw/'R9-debian.exit').write_text('0\n')
