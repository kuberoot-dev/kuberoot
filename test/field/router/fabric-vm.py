#!/usr/bin/env python3
"""Unprivileged equal-product fabric; run exclusively in the workshop pod."""
import argparse,json,subprocess,time,os
from pathlib import Path
p=argparse.ArgumentParser();p.add_argument('name',choices=['client','gateway','sink']);a=p.parse_args()
root=Path('/work/vms'); d=root/a.name;d.mkdir(exist_ok=True)
if (d/'pid').exists() and Path('/proc/'+(d/'pid').read_text().strip()).exists():raise SystemExit('VM already running')
base=Path('/work/field/images/linux-tools.qcow2')
if not base.exists():raise SystemExit('Prepare offline Linux tools base first')
if not (d/'sys.qcow2').exists():subprocess.run(['qemu-img','create','-q','-f','qcow2','-F','qcow2','-b',str(base),str(d/'sys.qcow2')],check=True)
if not (d/'vars.fd').exists():subprocess.run(['cp','/usr/share/OVMF/OVMF_VARS_4M.fd',str(d/'vars.fd')],check=True)
port={'client':2231,'gateway':2232,'sink':2233}[a.name]
key=Path('/work/field/private/id_ed25519.pub').read_text().strip()
(d/'user-data').write_text('#cloud-config\n'+json.dumps({'hostname':a.name,'disable_root':False,'users':[{'name':'root','ssh_authorized_keys':[key]}]}))
(d/'meta-data').write_text('instance-id: field-fabric-'+a.name+'\nlocal-hostname: '+a.name+'\n')
(d/'network-config').write_text(json.dumps({'version':2,'ethernets':{'mgmt0':{'match':{'macaddress':'52:54:00:30:00:01'},'set-name':'mgmt0','dhcp4':True},'test0':{'match':{'macaddress':f'52:54:00:40:{port%100:02x}:02'},'set-name':'test0'},'direct0':{'match':{'macaddress':f'52:54:00:40:{port%100:02x}:03'},'set-name':'direct0'}}}))
subprocess.run(['cloud-localds','--network-config='+str(d/'network-config'),str(d/'seed.img'),str(d/'user-data'),str(d/'meta-data')],check=True)
segments={'client':[(31,'lan'),(33,'direct')],'gateway':[(32,'site'),(34,'wan')],'sink':[(34,'wan'),(33,'direct')]}[a.name]
args=['qemu-system-x86_64','-name',a.name,'-enable-kvm','-cpu','host','-smp',os.environ.get('ENDPOINT_VCPU','1'),'-m',os.environ.get('ENDPOINT_RAM_MIB','768'),'-machine','q35','-boot','order=c',
 '-drive','if=pflash,format=raw,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.fd',
 '-drive',f'if=pflash,format=raw,file={d}/vars.fd','-drive',f'file={d}/sys.qcow2,format=qcow2,if=none,id=system,cache=none','-device','virtio-blk-pci,drive=system,bootindex=1',
 '-drive',f'file={d}/seed.img,format=raw,if=virtio,readonly=on',
 '-netdev',f'user,id=management,hostfwd=tcp:127.0.0.1:{port}-:22','-device',f'virtio-net-pci,romfile=,netdev=management,mac=52:54:00:30:00:01']
for idx,(seg,label) in enumerate(segments,2):
 args+=['-netdev',f'socket,id={label},mcast=230.31.0.{seg}:130{seg},localaddr=127.0.0.1','-device',f'virtio-net-pci,romfile=,id={label},netdev={label},mac=52:54:00:40:{port%100:02x}:{idx:02x}']
if a.name=='gateway' and os.environ.get('FIELD_PHASE1_PEER')=='1':
 args+=['-netdev','socket,id=phase1,mcast=230.0.0.3:12003,localaddr=127.0.0.1','-device','virtio-net-pci,romfile=,netdev=phase1,mac=52:54:00:40:20:04']
args+=['-chardev',f'socket,id=ser,path={d}/serial.sock,server=on,wait=off,logfile={d}/console.log,logappend=on','-serial','chardev:ser','-qmp',f'unix:{d}/qmp.sock,server=on,wait=off','-display','none','-daemonize','-pidfile',str(d/'pid')]
(d/'launch.json').write_text(json.dumps({'started_at':time.time(),'argv':args},indent=2)+'\n');subprocess.run(args,check=True)
