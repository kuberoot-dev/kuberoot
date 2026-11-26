#!/usr/bin/env python3
"""Recompress stopped owned Phase-1 evidence without changing logical bytes."""
import datetime,hashlib,json,pathlib,shutil,subprocess
root=pathlib.Path('/work');manifest=root/'field-edge/phase1-lossless-compression.jsonl'
paths=[p for name in ['media-demo','fleet','stand','upgrade-demo','fix-validation'] for p in (root/name).glob('*/sys.qcow2')]
done={json.loads(x)['source'] for x in manifest.read_text().splitlines()} if manifest.exists() else set()
for source in sorted(paths,key=lambda p:p.stat().st_blocks):
 if str(source) in done:continue
 for p in pathlib.Path('/proc').glob('[0-9]*/cmdline'):
  try:a=p.read_bytes().decode().split('\0')
  except (OSError,UnicodeError):continue
  if a[0].endswith('qemu-system-x86_64') and any(str(source) in x for x in a):raise SystemExit('Evidence disk is still attached: '+str(source))
 info=json.loads(subprocess.check_output(['qemu-img','info','--output=json',str(source)],text=True));assert not info.get('backing-filename'),'Backing requires separate review'
 before=source.stat().st_blocks*512;assert shutil.disk_usage(root).free>before+512*1024**2
 oldhash=hashlib.file_digest(source.open('rb'),'sha256').hexdigest();temporary=source.with_name('sys.lossless-compressed.partial');assert not temporary.exists()
 subprocess.run(['qemu-img','convert','-c','-f','qcow2','-O','qcow2',str(source),str(temporary)],check=True)
 subprocess.run(['qemu-img','compare','-f','qcow2','-F','qcow2',str(source),str(temporary)],check=True)
 after=temporary.stat().st_blocks*512;newhash=hashlib.file_digest(temporary.open('rb'),'sha256').hexdigest()
 if after<before:temporary.replace(source)
 else:temporary.unlink();after=before;newhash=oldhash
 row={'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'source':str(source),'old_file_sha256':oldhash,'new_file_sha256':newhash,'allocated_before':before,'allocated_after':after,'virtual_size':info['virtual-size'],'logical_bytes_compared_equal':True,'method':'qemu-img compressed re-encoding; no guest contents removed'}
 with manifest.open('a') as f:f.write(json.dumps(row)+'\n')
 print(json.dumps(row),flush=True)
