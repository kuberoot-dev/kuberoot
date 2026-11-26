#!/usr/bin/env python3
"""Unprivileged equal-product fabric; run exclusively in the workshop pod."""
import argparse,json,subprocess,time
from pathlib import Path
p=argparse.ArgumentParser();p.add_argument('name',choices=['client','gateway']);a=p.parse_args()
root=Path('/work/vms'); d=root/a.name;d.mkdir(exist_ok=True)
if (d/'pid').exists() and Path('/proc/'+(d/'pid').read_text().strip()).exists():raise SystemExit('VM already running')
base=Path('/work/field/images/linux-tools.qcow2')
if not base.exists():raise SystemExit('Prepare offline Linux tools base first')
if not (d/'sys.qcow2').exists():subprocess.run(['qemu-img','create','-q','-f','qcow2','-F','qcow2','-b',str(base),str(d/'sys.qcow2')],check=True)
if not (d/'vars.fd').exists():subprocess.run(['cp','/usr/share/OVMF/OVMF_VARS_4M.fd',str(d/'vars.fd')],check=True)
port=2231 if a.name=='client' else 2232
segments=[(31,'lan'),(33,'direct')] if a.name=='client' else [(32,'wan'),(33,'direct')]
args=['qemu-system-x86_64','-name',a.name,'-enable-kvm','-cpu','host','-smp','1','-m','768','-machine','q35',
 '-drive','if=pflash,format=raw,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.fd',
 '-drive',f'if=pflash,format=raw,file={d}/vars.fd','-drive',f'file={d}/sys.qcow2,format=qcow2,if=virtio,cache=none',
 '-netdev',f'user,id=management,hostfwd=tcp:127.0.0.1:{port}-:22','-device',f'virtio-net-pci,netdev=management,mac=52:54:00:40:{port%100:02x}:01']
for idx,(seg,label) in enumerate(segments,2):
 args+=['-netdev',f'socket,id={label},mcast=230.31.0.{seg}:130{seg},localaddr=127.0.0.1','-device',f'virtio-net-pci,id={label},netdev={label},mac=52:54:00:40:{port%100:02x}:{idx:02x}']
args+=['-chardev',f'socket,id=ser,path={d}/serial.sock,server=on,wait=off,logfile={d}/console.log,logappend=on','-serial','chardev:ser','-qmp',f'unix:{d}/qmp.sock,server=on,wait=off','-display','none','-daemonize','-pidfile',str(d/'pid')]
(d/'launch.json').write_text(json.dumps({'started_at':time.time(),'argv':args},indent=2)+'\n');subprocess.run(args,check=True)
