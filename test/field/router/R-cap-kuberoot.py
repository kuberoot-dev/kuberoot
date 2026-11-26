#!/usr/bin/env python3
"""Negative native API capability gates; no inference from installed binaries."""
import json,os,subprocess,time
from pathlib import Path
os.environ['KUBECONFIG']='/work/cluster-r1.conf'
features={'VRRP/stateful failover':{'vrrp':{'virtualAddress':'192.0.2.1'}},'WireGuard':{'wireguard':{'listenPort':51820}},'IPsec':{'ipsec':{'peer':'192.0.2.2'}},'PPPoE':{'pppoe':{'service':'field'}},'DHCPv6/PD':{'dhcpv6PrefixDelegation':{'request':True}},'IPv6 RA':{'routerAdvertisement':{'enabled':True}}}
rows=[]
for feature,spec in features.items():
 obj={'apiVersion':'router.kuberoot.dev/v1alpha1','kind':'Interface','metadata':{'name':'field-capability-gate'},'spec':{'link':'eth1',**spec}}
 cmd=['kubectl','apply','--dry-run=server','--validate=strict','-f','-']
 print(time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime()),feature,flush=True)
 print('$ '+' '.join(cmd),flush=True);print(json.dumps(obj),flush=True)
 p=subprocess.run(cmd,input=json.dumps(obj),capture_output=True,text=True);print(p.stdout+p.stderr,flush=True)
 rows.append({'feature':feature,'exit':p.returncode,'stdout':p.stdout,'stderr':p.stderr,'input':obj,'native_api_rejected':p.returncode!=0 and 'unknown field' in p.stderr})
Path('/work/field/raw/R-cap-kuberoot.json').write_text(json.dumps(rows,indent=2)+'\n')
