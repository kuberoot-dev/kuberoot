#!/usr/bin/env python3
"""Two actual BIRD upstreams on distinct VLANs, FRR RIB/FIB and sampled traffic."""
import json,subprocess,time,ipaddress,os
from pathlib import Path
raw=Path('/work/field/raw');ram_label=os.environ.get('FIELD_DUT_RAM_GIB','2')+'GiB'
def guest(vm,cmd,timeout=90):
 print(time.strftime('%FT%TZ',time.gmtime()),'$ guest-command',vm,cmd,flush=True)
 p=subprocess.run(['/work/field/guest-command',vm,cmd],capture_output=True,text=True,timeout=timeout);print(p.stdout+p.stderr,flush=True);return p
def put(vm,path,data):
 print('$ guest-command',vm,'cat >',path,'(generated config on stdin)',flush=True)
 subprocess.run(['/work/field/guest-command',vm,'cat >'+path],input=data,text=True,check=True)
while not (raw/'R5-debian.exit').exists():time.sleep(3)
guest('client','ip route replace 10.64.0.0/12 via 192.168.10.1')
put('debian','/etc/systemd/network/04-field-upstream2.network','[Match]\nName=upstream2\n[Network]\nAddress=198.18.20.1/24\nAddress=fd42:2::1/64\nLinkLocalAddressing=ipv6\n')
guest('debian','cp /etc/frr/frr.conf /tmp/R7-frr-before.conf; nft list ruleset >/tmp/R7-nft-before; nft flush ruleset; ip link add link enp0s2 name upstream2 type vlan id 20; ip addr add 198.18.20.1/24 dev upstream2; ip link set upstream2 up; ip -6 addr add fd42:1::1/64 dev enp0s2; ip -6 addr add fd42:2::1/64 dev upstream2')
guest('gateway','systemctl stop bird; ip netns add upstream2; ip link add link test0 name upstream2 type vlan id 20; ip link set upstream2 netns upstream2; ip netns exec upstream2 ip link set lo up; ip netns exec upstream2 ip addr add 198.18.20.2/24 dev upstream2; ip netns exec upstream2 ip link set upstream2 up; ip netns exec upstream2 ip route add default via 198.18.20.1; ip -6 addr add fd42:1::2/64 dev test0; ip netns exec upstream2 ip -6 addr add fd42:2::2/64 dev upstream2; mkdir -p /run/field-bird')
config='frr defaults traditional\nhostname field-debian\nservice integrated-vtysh-config\nrouter bgp 65000\n bgp router-id 198.18.0.1\n no bgp ebgp-requires-policy\n no bgp network import-check\n'
for addr,asn in [('198.18.0.2',65001),('198.18.20.2',65002),('fd42:1::2',65001),('fd42:2::2',65002)]:config+=f' neighbor {addr} remote-as {asn}\n neighbor {addr} timers 3 9\n'
config+=' address-family ipv4 unicast\n  neighbor 198.18.0.2 activate\n  neighbor 198.18.20.2 activate\n exit-address-family\n address-family ipv6 unicast\n  neighbor fd42:1::2 activate\n  neighbor fd42:2::2 activate\n exit-address-family\n'
put('debian','/etc/frr/frr.conf',config);guest('debian','systemctl restart frr')
for count in [int(x) for x in os.environ.get('FIELD_PREFIX_COUNTS','10000,100000').split(',')]:
 ipv6=int(os.environ.get('FIELD_IPV6_COUNT','0'))
 for side,local in [(1,'198.18.0.2'),(2,'198.18.20.2')]:
  path=f'/work/field/upstream-{side}.conf'
  subprocess.run(['python3','/work/field/bgp-prefix-generator.py',path,'--ipv4',str(count),'--ipv6',str(ipv6),'--asn',str(65000+side),'--local',local,'--peer','198.18.0.1' if side==1 else '198.18.20.1'],check=True)
  put('gateway',f'/tmp/upstream-{side}.conf',Path(path).read_text())
  prefix='' if side==1 else 'ip netns exec upstream2 '
  guest('gateway',f'if test -e /run/field-bird/{side}.pid; then kill $(cat /run/field-bird/{side}.pid); sleep 1; fi; {prefix}bird -c /tmp/upstream-{side}.conf -s /run/field-bird/{side}.ctl -P /run/field-bird/{side}.pid')
 start=time.monotonic();rows=[]
 while time.monotonic()-start<300:
  t=time.monotonic();p=guest('debian',"vtysh -c 'show bgp ipv4 unicast summary json'; ip -4 route show proto bgp | wc -l; head -3 /proc/meminfo",30)
  rows.append({'elapsed_s':time.monotonic()-start,'observer_ssh_s':time.monotonic()-t,'exit':p.returncode,'text':p.stdout,'stderr':p.stderr})
  # Counts recorded verbatim; parse exact FIB line after JSON object.
  lines=p.stdout.strip().splitlines();fib=int(lines[-4]) if len(lines)>=4 and lines[-4].isdigit() else None
  try:peers=json.JSONDecoder().raw_decode(p.stdout.lstrip())[0]['peers'];both=all(peers.get(ip,{}).get('pfxRcd')==count and peers[ip].get('state')=='Established' for ip in ['198.18.0.2','198.18.20.2'])
  except (ValueError,KeyError):both=False
  v6ok=True
  if ipv6:
   v6=guest('debian',"vtysh -c 'show bgp ipv6 unicast summary json'; ip -6 route show proto bgp | wc -l")
   rows[-1]['ipv6_text']=v6.stdout;rows[-1]['ipv6_elapsed_s']=time.monotonic()-start
   try:
    v6peers=json.JSONDecoder().raw_decode(v6.stdout.lstrip())[0]['peers'];v6ok=int(v6.stdout.strip().splitlines()[-1])==ipv6 and all(v6peers.get(ip,{}).get('pfxRcd')==ipv6 for ip in ['fd42:1::2','fd42:2::2'])
   except (ValueError,KeyError):v6ok=False
  if fib==count and both and v6ok:break
  time.sleep(3)
 (raw/f'R7-debian-{count}-{ram_label}.json').write_text(json.dumps(rows,indent=2)+'\n')
 # Sample 1000 deterministic destinations; endpoints own these /32s.
 addresses=[str(ipaddress.IPv4Address(int(ipaddress.IPv4Address('10.64.0.0'))+i*(count//1000))) for i in range(1000)]
 for side in [1,2]:
  prefix='' if side==1 else 'ip netns exec upstream2 '
  put('gateway',f'/tmp/R7-samples-{side}.sh','\n'.join(prefix+'ip addr replace '+a+'/32 dev lo' for a in addresses)+'\n')
  guest('gateway',f'sh /tmp/R7-samples-{side}.sh')
 put('client','/tmp/R7-samples.txt','\n'.join(addresses)+'\n')
 p=guest('client','fping -c 1 -t 1000 -i 2 -f /tmp/R7-samples.txt',60)
 (raw/(f'R7-debian-{count}-samples'+('-'+ram_label if count>=800000 else '')+'.txt')).write_text(p.stdout+p.stderr)
 if ipv6:
  addresses6=[str(ipaddress.IPv6Address(int(ipaddress.IPv6Address('fd42:100::'))+i*(ipv6//1000))) for i in range(1000)]
  for side in [1,2]:
   prefix='' if side==1 else 'ip netns exec upstream2 '
   put('gateway',f'/tmp/R7-samples6-{side}.sh','\n'.join(prefix+'ip -6 addr replace '+a+'/128 dev lo' for a in addresses6)+'\n');guest('gateway',f'sh /tmp/R7-samples6-{side}.sh')
  guest('debian','sysctl -w net.ipv6.conf.all.forwarding=1; ip -6 addr replace fd42:10::1/64 dev enp0s3')
  guest('client','ip -6 addr replace fd42:10::2/64 dev test0; ip -6 route replace fd42:100::/64 via fd42:10::1')
  guest('gateway','ip -6 route replace fd42:10::/64 via fd42:1::1; ip netns exec upstream2 ip -6 route replace fd42:10::/64 via fd42:2::1')
  put('client','/tmp/R7-samples6.txt','\n'.join(addresses6)+'\n');p=guest('client','fping -6 -c 1 -t 1000 -i 2 -f /tmp/R7-samples6.txt',60);(raw/f'R7-debian-{count}-samples6-{ram_label}.txt').write_text(p.stdout+p.stderr)
(raw/('R7-debian-full'+os.environ.get('FIELD_DUT_RAM_GIB','2')+'.exit' if os.environ.get('FIELD_IPV6_COUNT') else 'R7-debian-small.exit')).write_text('0\n')
