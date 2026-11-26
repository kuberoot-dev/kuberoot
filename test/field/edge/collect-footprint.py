#!/usr/bin/env python3
"""Read actual artifact sizes and raw disk allocation; not guest filesystem usage."""
import argparse,datetime,json,pathlib
p=argparse.ArgumentParser();p.add_argument('product');p.add_argument('vm',type=pathlib.Path);p.add_argument('output',type=pathlib.Path);a=p.parse_args()
root=pathlib.Path('/work/field-edge')
if a.product=='kuberoot-v0.1.0':files=list(pathlib.Path('/work/release').glob('edge-amd64-*'));files=[f for f in files if f.suffix in ['.cpio','.squashfs','.efi']]
elif a.product.startswith('kuberoot-main'):files=[root/'build-main/out/amd64'/n for n in ['vmlinuz.efi','initrd.cpio','rootfs.squashfs','systemd-boot.efi']]
elif a.product.startswith('k3s'):files=[root/'downloads/ubuntu-24.04-server-cloudimg-amd64.img']
else:files=[root/'downloads/talos-v1.14.7-metal-amd64.iso']
assets=[{'path':str(f),'bytes':f.stat().st_size} for f in files]
disk=(a.vm/'system.raw').stat()
r={'product':a.product,'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'assets':assets,'asset_bytes_sum':sum(x['bytes'] for x in assets),'raw_disk_virtual_bytes':disk.st_size,'raw_disk_host_allocated_bytes':disk.st_blocks*512,'uefi_vars_bytes':(a.vm/'vars.fd').stat().st_size,'notes':['Raw allocation is host storage occupied by a sparse raw file; it is not guest RAM or guest filesystem used space.','Asset sum is the named boot/download artifacts only; it excludes later network pulls and does not measure total provisioning traffic.','k3s additionally downloaded its verified 81,678,498-byte binary; Kubernetes/add-on image pulls are not included.']}
if not a.product.startswith('k3s'):r['notes'].pop()
a.output.write_text(json.dumps(r,indent=2)+'\n');print(json.dumps(r,indent=2))
