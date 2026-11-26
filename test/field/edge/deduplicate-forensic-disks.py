#!/usr/bin/env python3
"""Losslessly share immutable blocks of stopped historical field-test disks.
No failed evidence is discarded: full logical bytes are compared before rename.
Run between measurements, not concurrently with a measured DUT workload.
"""
import datetime,hashlib,json,pathlib,subprocess
root=pathlib.Path('/work/field-edge')
base=root/'results/E-B2-kuberoot-v0.1.0/1/failed.qcow2'
paths=[root/f'results/E-B2-kuberoot-v0.1.0/{n}/failed.qcow2' for n in [2,3]]+[root/'wan-release/after-fault.qcow2',root/'wan-main/after-fault.qcow2']
assert pathlib.Path('/dev/kvm').exists() and base.is_file()
for p in pathlib.Path('/proc').glob('[0-9]*/cmdline'):
 try:args=p.read_bytes().decode().split('\0')
 except (OSError,UnicodeError):continue
 if args[0].endswith('qemu-system-x86_64') and '-name' in args and args[args.index('-name')+1].startswith('field-edge-'):raise SystemExit('DUT still running: execute only after shutdown')
basehash=hashlib.file_digest(base.open('rb'),'sha256').hexdigest();manifest=root/'forensic-block-sharing.jsonl'
for source in paths:
 if not source.exists():continue
 info=json.loads(subprocess.check_output(['qemu-img','info','--output=json',str(source)],text=True))
 assert not info.get('backing-filename') or info.get('full-backing-filename')==str(base),'Unexpected existing backing chain'
 previous=[json.loads(x) for x in manifest.read_text().splitlines()] if manifest.exists() else []
 if any(x.get('source')==str(source) and x.get('method')=='safe-empty-overlay-rebase' for x in previous):continue
 temporary=source.with_suffix('.dedup.partial');assert not temporary.exists()
 before=source.stat().st_blocks*512
 subprocess.run(['python3',str(pathlib.Path(__file__).with_name('share-disk-blocks.py')),str(source),str(base),str(temporary),'--source-format','qcow2'],check=True)
 subprocess.run(['qemu-img','compare','-f','qcow2','-F','qcow2',str(source),str(temporary)],check=True)
 row={'method':'safe-empty-overlay-rebase','utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'source':str(source),'immutable_backing':str(base),'backing_sha256':basehash,'logical_bytes_compared_equal':True,'allocated_before':before,'allocated_after':temporary.stat().st_blocks*512}
 temporary.replace(source)
 with manifest.open('a') as f:f.write(json.dumps(row)+'\n')
 print(json.dumps(row),flush=True)
assert hashlib.file_digest(base.open('rb'),'sha256').hexdigest()==basehash
