#!/usr/bin/env python3
"""Two actual BIRD upstreams on distinct VLANs, FRR RIB/FIB and sampled traffic."""
import json,subprocess,time,ipaddress
from pathlib import Path
raw=Path('/work/field/raw')
def guest(vm,cmd,timeout=90):
 print(time.strftime('%FT%TZ',time.gmtime()),'$ guest-command',vm,cmd,flush=True)
 p=subprocess.run(['/work/field/guest-command',vm,cmd],capture_output=True,text=True,timeout=timeout);print(p.stdout+p.stderr,flush=True);return p
def put(vm,path,data):
 print('$ guest-command',vm,'cat >',path,'(generated config on stdin)',flush=True)
 subprocess.run(['/work/field/guest-command',vm,'cat >'+path],input=data,text=True,check=True)
while not (raw/'R5-debian.exit').exists():time.sleep(3)
guest('debian','cp /etc/frr/frr.conf /tmp/R7-frr-before.conf; nft list ruleset >/tmp/R7-nft-before; nft flush ruleset; ip link add link enp0s2 name upstream2 type vlan id 20; ip addr add 198.18.20.1/24 dev upstream2; ip link set upstream2 up; ip -6 addr add fd42:1::1/64 dev enp0s2; ip -6 addr add fd42:2::1/64 dev upstream2')
guest('gateway','systemctl stop bird; ip netns add upstream2; ip link add link test0 name upstream2 type vlan id 20; ip link set upstream2 netns upstream2; ip netns exec upstream2 ip link set lo up; ip netns exec upstream2 ip addr add 198.18.20.2/24 dev upstream2; ip netns exec upstream2 ip link set upstream2 up; ip -6 addr add fd42:1::2/64 dev test0; ip netns exec upstream2 ip -6 addr add fd42:2::2/64 dev upstream2; mkdir -p /run/field-bird')
config='frr defaults traditional\nhostname field-debian\nservice integrated-vtysh-config\nrouter bgp 65000\n bgp router-id 198.18.0.1\n no bgp ebgp-requires-policy\n no bgp network import-check\n'
for addr,asn in [('198.18.0.2',65001),('198.18.20.2',65002),('fd42:1::2',65001),('fd42:2::2',65002)]:config+=f' neighbor {addr} remote-as {asn}\n neighbor {addr} timers 3 9\n'
config+=' address-family ipv4 unicast\n  neighbor 198.18.0.2 activate\n  neighbor 198.18.20.2 activate\n exit-address-family\n address-family ipv6 unicast\n  neighbor fd42:1::2 activate\n  neighbor fd42:2::2 activate\n exit-address-family\n'
put('debian','/etc/frr/frr.conf',config);guest('debian','systemctl restart frr')
for count in [10000,100000]:
 for side,local in [(1,'198.18.0.2'),(2,'198.18.20.2')]:
  path=f'/work/field/upstream-{side}.conf'
  subprocess.run(['python3','/work/field/bgp-prefix-generator.py',path,'--ipv4',str(count),'--asn',str(65000+side),'--local',local,'--peer','198.18.0.1' if side==1 else '198.18.20.1'],check=True)
  put('gateway',f'/tmp/upstream-{side}.conf',Path(path).read_text())
  prefix='' if side==1 else 'ip netns exec upstream2 '
  guest('gateway',f'if test -e /run/field-bird/{side}.pid; then kill $(cat /run/field-bird/{side}.pid); sleep 1; fi; {prefix}bird -c /tmp/upstream-{side}.conf -s /run/field-bird/{side}.ctl -P /run/field-bird/{side}.pid')
 start=time.monotonic();rows=[]
 while time.monotonic()-start<300:
  t=time.monotonic();p=guest('debian',"vtysh -c 'show bgp ipv4 unicast summary json'; ip -4 route show proto bgp | wc -l; head -3 /proc/meminfo",30)
  rows.append({'elapsed_s':time.monotonic()-start,'observer_ssh_s':time.monotonic()-t,'exit':p.returncode,'text':p.stdout,'stderr':p.stderr})
  # Counts recorded verbatim; parse exact FIB line after JSON object.
  lines=p.stdout.strip().splitlines();fib=int(lines[-4]) if len(lines)>=4 and lines[-4].isdigit() else None
  if fib==count:break
  time.sleep(3)
 (raw/f'R7-debian-{count}-2GiB.json').write_text(json.dumps(rows,indent=2)+'\n')
 # Sample 1000 deterministic destinations; endpoints own these /32s.
 addresses=[str(ipaddress.IPv4Address(int(ipaddress.IPv4Address('10.64.0.0'))+i*(count//1000))) for i in range(1000)]
 for side in [1,2]:
  prefix='' if side==1 else 'ip netns exec upstream2 '
  put('gateway',f'/tmp/R7-samples-{side}.sh','\n'.join(prefix+'ip addr replace '+a+'/32 dev lo' for a in addresses)+'\n')
  guest('gateway',f'sh /tmp/R7-samples-{side}.sh')
 put('client','/tmp/R7-samples.txt','\n'.join(addresses)+'\n')
 p=guest('client','fping -c 1 -t 1000 -i 2 -f /tmp/R7-samples.txt',60)
 (raw/f'R7-debian-{count}-samples.txt').write_text(p.stdout+p.stderr)
(raw/'R7-debian-small.exit').write_text('0\n')
