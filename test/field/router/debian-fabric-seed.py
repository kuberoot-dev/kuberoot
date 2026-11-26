#!/usr/bin/env python3
"""Generate private cloud-init media on PVC with explicit fabric NIC identity."""
import json,subprocess
from pathlib import Path
d=Path('/work/vms/debian')
key=Path('/work/field/private/id_ed25519.pub').read_text().strip()
config={'hostname':'field-debian','disable_root':False,'users':[{'name':'root','ssh_authorized_keys':[key]}]}
network={'version':2,'ethernets':{
 'enp0s2':{'match':{'macaddress':'52:54:00:30:00:01'},'set-name':'enp0s2','addresses':['198.18.0.1/24'],'routes':[{'to':'0.0.0.0/0','via':'198.18.0.2'}]},
 'enp0s3':{'match':{'macaddress':'52:54:00:30:00:02'},'set-name':'enp0s3','addresses':['192.168.10.1/24']},
 'enp0s4':{'match':{'macaddress':'52:54:00:30:00:03'},'set-name':'enp0s4','dhcp4':True,'dhcp4-overrides':{'use-routes':False,'use-dns':False}}}}
(d/'fabric-user-data').write_text('#cloud-config\n'+json.dumps(config))
(d/'fabric-meta-data').write_text('instance-id: field-debian-fabric-01\nlocal-hostname: field-debian\n')
(d/'fabric-network').write_text(json.dumps(network))
subprocess.run(['cloud-localds','--network-config='+str(d/'fabric-network'),str(d/'seed.img'),str(d/'fabric-user-data'),str(d/'fabric-meta-data')],check=True)
