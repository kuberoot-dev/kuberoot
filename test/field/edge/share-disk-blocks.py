#!/usr/bin/env python3
"""Create a verified lossless delta by safely rebasing an empty overlay.
convert -B alone does not compare allocated source clusters with its backing.
This helper never modifies source/backing and never removes an original disk.
"""
import argparse,json,pathlib,subprocess
p=argparse.ArgumentParser();p.add_argument('source',type=pathlib.Path);p.add_argument('backing',type=pathlib.Path);p.add_argument('output',type=pathlib.Path);p.add_argument('--source-format',choices=['raw','qcow2'],required=True);a=p.parse_args()
root=pathlib.Path('/work');assert all(x.is_relative_to(root) for x in [a.source,a.backing,a.output]);assert a.source.is_file() and a.backing.is_file() and not a.output.exists()
for path in pathlib.Path('/proc').glob('[0-9]*/cmdline'):
 try:argv=path.read_bytes().decode().split('\0')
 except (OSError,UnicodeError):continue
 if argv[0].endswith('qemu-system-x86_64') and any(str(a.source) in arg or str(a.backing) in arg for arg in argv):raise SystemExit('source/backing is attached to a running VM')
temporary=a.output.with_name(a.output.name+'.rebase.partial');assert not temporary.exists()
# Initially this empty overlay reads exactly the source through its backing.
subprocess.run(['qemu-img','create','-f','qcow2','-F',a.source_format,'-b',str(a.source),str(temporary)],check=True)
# SAFE rebase compares old and new backings, materializing only changed data.
subprocess.run(['qemu-img','rebase','-f','qcow2','-F','qcow2','-b',str(a.backing),str(temporary)],check=True)
subprocess.run(['qemu-img','convert','-c','-f','qcow2','-O','qcow2','-F','qcow2','-B',str(a.backing),str(temporary),str(a.output)],check=True)
subprocess.run(['qemu-img','compare','-f',a.source_format,'-F','qcow2',str(a.source),str(a.output)],check=True)
temporary.unlink()
print(json.dumps({'source':str(a.source),'immutable_backing':str(a.backing),'output':str(a.output),'logical_bytes_compared_equal':True,'source_allocated_bytes':a.source.stat().st_blocks*512,'delta_allocated_bytes':a.output.stat().st_blocks*512}))
