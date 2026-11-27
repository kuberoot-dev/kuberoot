#!/usr/bin/env python3
import json,os,subprocess,time
from pathlib import Path
os.environ['KUBECONFIG']='/work/cluster-r1.conf';raw=Path('/work/field/raw');rows=[]
def run(cmd,inp=None):
 print(time.strftime('%FT%TZ',time.gmtime()),'$',' '.join(cmd),flush=True)
 p=subprocess.run(cmd,input=inp,capture_output=True,text=True,timeout=40);print(p.stdout+p.stderr,flush=True);rows.append({'at':time.time(),'command':cmd,'exit':p.returncode,'stdout':p.stdout,'stderr':p.stderr});return p
def g(cmd):return run(['/work/field/guest-command','gateway',cmd])
def put(path,text):return run(['/work/field/guest-command','gateway','cat >'+path],text)
def k(args,obj=None):return run(['kubectl','--request-timeout=5s']+args,json.dumps(obj) if obj else None)
def apply(kind,name,spec):return k(['apply','-f','-'],{'apiVersion':'router.kuberoot.dev/v1alpha1','kind':kind,'metadata':{'name':name},'spec':spec})
k(['patch','safeguard','default','--type=merge','-p','{"spec":{"confirmWithin":"600s"}}'])
g('for p in /run/field-r8/*.pid; do test ! -f "$p" || kill $(cat "$p") 2>/dev/null; done; ip netns del fieldobserver 2>/dev/null; ip netns add fieldobserver; ip link add link enp0s6 fieldobserver type macvlan mode bridge; ip link set fieldobserver netns fieldobserver; ip netns exec fieldobserver ip link set lo up; ip netns exec fieldobserver ip addr add 10.255.0.5/24 dev fieldobserver; ip netns exec fieldobserver ip link set fieldobserver up; mkdir -p /run/field-r8')
# U2 uses another local IP/port-179 listener, with a distinct BIRD instance/ASN.
g('ip addr add 10.255.0.4/24 dev enp0s6')
base='log stderr all; router id {local}; protocol device {{}}\n'
for name,local,asn,ns,routes in [('customer','10.255.0.3',65101,'','route 203.0.113.0/24 blackhole; route 203.0.113.128/25 blackhole; route 0.0.0.0/0 blackhole;'),('transit','10.255.0.4',65102,'','route 198.51.100.0/24 blackhole;'),('observer','10.255.0.5',65103,'ip netns exec fieldobserver ','')]:
 text=base.format(local=local)+'protocol static injected { ipv4; '+routes+' }\n'+f'protocol bgp dut {{ local {local} as {asn}; neighbor 10.255.0.1 as 65000; strict bind on; ipv4 {{ import all; export where source = RTS_STATIC; }}; }}\n'
 put('/root/R8-'+name+'.conf',text);g(ns+f'bird -c /root/R8-{name}.conf -s /run/field-r8/{name}.ctl -P /run/field-r8/{name}.pid')
 apply('BGPPeer','field-'+name,{'address':local,'asn':asn,'importPolicy':{'prefixes':['203.0.113.0/24'] if name=='customer' else ['198.51.100.0/24'] if name=='transit' else []},'exportPolicy':{'prefixes':['192.0.2.0/24']}})
apply('BGPRouter','default',{'asn':65000,'routerID':'10.255.0.1','announce':['192.0.2.0/24'],'import':'All'})
for _ in range(25):
 p=k(['get','bgppeers','-o','json'])
 try:ready=all(x['status'].get('state')=='Established' for x in json.loads(p.stdout)['items'] if x['metadata']['name'].startswith('field-'))
 except Exception:ready=False
 if ready:break
 time.sleep(2)
g('nohup tcpdump -i enp0s6 -w /root/R8-kuberoot.pcap tcp port 179 >/root/R8-tcpdump.log 2>&1 </dev/null &')
g('birdc -s /run/field-r8/observer.ctl show route all');g('birdc -s /run/field-r8/transit.ctl show route all')
start=time.monotonic();k(['patch','bgppeer','field-observer','--type=merge','-p','{"spec":{"exportPolicy":{"prefixes":[]}}}']);withdraw=None
for _ in range(20):
 p=g('birdc -s /run/field-r8/observer.ctl show route all')
 if p.returncode==0 and '192.0.2.0/24' not in p.stdout:withdraw=time.monotonic()-start;break
 time.sleep(.5)
k(['patch','bgppeer','field-customer','--type=json','-p','[{"op":"remove","path":"/spec/importPolicy"},{"op":"add","path":"/spec/maxPrefixes","value":1000}]'])
# Generate 1001 accepted prefixes, with an exact independent BIRD count.
text=base.format(local='10.255.0.3')+'protocol static injected { ipv4; '+''.join(f'route 10.64.{i//256}.{i%256}/32 blackhole; ' for i in range(1001))+' }\nprotocol bgp dut { local 10.255.0.3 as 65101; neighbor 10.255.0.1 as 65000; strict bind on; ipv4 { import all; export where source = RTS_STATIC; }; }\n'
put('/root/R8-customer.conf',text);start=time.monotonic();g('birdc -s /run/field-r8/customer.ctl configure');limit=None
for _ in range(24):
 p=run(['python3','/work/field/console.py','r1','birdc -s /run/kuberoot/router/bird.ctl show protocols all','--timeout','20'])
 if 'limit' in p.stdout.lower() and ('disabled' in p.stdout.lower() or 'exceed' in p.stdout.lower()):limit=time.monotonic()-start;break
 time.sleep(.25)
summary={'export_withdraw_observed_s':withdraw,'max_prefix_observed_s':limit,'scope':'native BGPPeer policies; snapshots, packet capture begins after establishment; no exhaustive UPDATE audit yet'}
(raw/'R8-kuberoot-policy.json').write_text(json.dumps(rows,indent=2)+'\n');(raw/'R8-kuberoot-policy.summary.json').write_text(json.dumps(summary,indent=2)+'\n');print(json.dumps(summary),flush=True)
k(['delete','bgppeers','field-customer','field-transit','field-observer']);time.sleep(3)
p=k(['get','safeguard','default','-o','json']);s=json.loads(p.stdout)['status'];k(['patch','safeguard','default','--type=merge','-p',json.dumps({'spec':{'confirmWithin':'60s','confirm':s.get('pending') or s['running']}})])
