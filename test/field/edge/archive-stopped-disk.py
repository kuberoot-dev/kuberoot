#!/usr/bin/env python3
"""Stream an owned, stopped Phase-1 disk to Mac; verify SHA256 before PVC removal.
No VM tools run locally. The local archive is a reversible capacity workaround.
"""
import datetime,hashlib,json,os,pathlib,shlex,subprocess,sys
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
expected_size=int(subprocess.check_output(base+['stat','-c','%s',source],env=env,text=True))
destination.parent.mkdir(parents=True,exist_ok=True);partial=destination.with_suffix('.partial')
resumed_from=partial.stat().st_size if partial.exists() else 0
prefix_script="""import hashlib,sys
h=hashlib.sha256();remaining=int(sys.argv[2])
with open(sys.argv[1],'rb') as f:
 while remaining:
  data=f.read(min(8*1024**2,remaining));assert data;h.update(data);remaining-=len(data)
print(h.hexdigest())
"""
stream_script="""import shutil,sys,time
with open(sys.argv[1],'rb') as f:
 f.seek(int(sys.argv[2]));shutil.copyfileobj(f,sys.stdout.buffer,1024*1024)
sys.stdout.buffer.flush();time.sleep(.5)
"""
for transfer_attempt in range(1,4):
 size=partial.stat().st_size if partial.exists() else 0;assert size<=expected_size
 digest=hashlib.sha256()
 if size:
  with partial.open('rb') as existing:
   for data in iter(lambda:existing.read(8*1024**2),b''):digest.update(data)
  prefix=subprocess.check_output(base+['python3','-c',prefix_script,source,str(size)],env=env,text=True).strip()
  assert digest.hexdigest()==prefix,'partial is not an exact source prefix; original and partial preserved'
 if size==expected_size:break
 process=subprocess.Popen(base+['python3','-c',stream_script,source,str(size)],env=env,stdout=subprocess.PIPE)
 with partial.open('ab') as f:
  os.fchmod(f.fileno(),0o600)
  while data:=process.stdout.read(8*1024**2):f.write(data);digest.update(data);size+=len(data)
  f.flush();os.fsync(f.fileno())
 assert process.wait()==0,'transport failed; original and partial preserved'
 if size==expected_size:break
 print(json.dumps({'short_stream_bytes':size,'expected_bytes':expected_size,'attempt':transfer_attempt,'action':'validate exact prefix before retry'}),flush=True)
assert size==expected_size,'archive size mismatch; original preserved'
assert digest.hexdigest()==expected,'archive digest mismatch; original preserved'
partial.rename(destination)
subprocess.run(base+['python3','-c',guard,source],env=env,check=True)
assert subprocess.check_output(base+['sha256sum',source],env=env,text=True).split()[0]==expected
manifest={'source_pod':'kr-edge-stand','source':source,'archive':str(destination),'bytes':size,'sha256':expected,'transfer_attempts':transfer_attempt,'resumed_from_bytes':resumed_from,'verified_utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'restore':'KUBECONFIG=~/workshop.txt kubectl --context admin@workshop -n tenant-kuberoot cp '+shlex.quote(str(destination))+' kr-edge-stand:'+source,'original_removed':False}
manifest_path=destination.with_suffix('.json');manifest_path.write_text(json.dumps(manifest,indent=2)+'\n')
subprocess.run(base+['rm','--',source],env=env,check=True)
manifest['original_removed']=True;manifest_path.write_text(json.dumps(manifest,indent=2)+'\n');print(json.dumps(manifest),flush=True)
