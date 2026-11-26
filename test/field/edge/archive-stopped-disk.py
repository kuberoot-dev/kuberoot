#!/usr/bin/env python3
"""Stream an owned, stopped Phase-1 disk to Mac; verify SHA256 before PVC removal.
No VM tools run locally. The local archive is a reversible capacity workaround.
"""
import datetime,hashlib,json,os,pathlib,subprocess,sys
source=sys.argv[1];destination=pathlib.Path(sys.argv[2]);assert source.startswith('/work/') and source.endswith('/sys.qcow2');assert not destination.exists()
env=dict(os.environ,KUBECONFIG='/Users/tym83/workshop.txt')
base=['kubectl','--context','admin@workshop','-n','tenant-kuberoot','exec','kr-edge-stand','--']
guard='''import pathlib,sys
for p in pathlib.Path('/proc').glob('[0-9]*/cmdline'):
 try:a=p.read_bytes().split(b'\\0')
 except OSError:continue
 if a and a[0].endswith(b'qemu-system-x86_64') and any(sys.argv[1].encode() in x for x in a):raise SystemExit('disk is attached')
'''
subprocess.run(base+['python3','-c',guard,source],env=env,check=True)
expected=subprocess.check_output(base+['sha256sum',source],env=env,text=True).split()[0]
destination.parent.mkdir(parents=True,exist_ok=True);partial=destination.with_suffix('.partial');digest=hashlib.sha256();size=0
process=subprocess.Popen(base+['cat',source],env=env,stdout=subprocess.PIPE)
with partial.open('xb') as f:
 os.fchmod(f.fileno(),0o600)  # Full guest disks include private cluster state.
 while data:=process.stdout.read(8*1024**2):f.write(data);digest.update(data);size+=len(data)
 f.flush();os.fsync(f.fileno())
assert process.wait()==0
assert digest.hexdigest()==expected,'archive digest mismatch; original preserved'
partial.rename(destination)
subprocess.run(base+['python3','-c',guard,source],env=env,check=True)
assert subprocess.check_output(base+['sha256sum',source],env=env,text=True).split()[0]==expected
manifest={'source_pod':'kr-edge-stand','source':source,'archive':str(destination),'bytes':size,'sha256':expected,'verified_utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'restore':'kubectl cp archive kr-edge-stand:'+source,'original_removed':False}
manifest_path=destination.with_suffix('.json');manifest_path.write_text(json.dumps(manifest,indent=2)+'\n')
subprocess.run(base+['rm','--',source],env=env,check=True)
manifest['original_removed']=True;manifest_path.write_text(json.dumps(manifest,indent=2)+'\n');print(json.dumps(manifest),flush=True)
