#!/usr/bin/env python3
"""Retire only copied, successful E1 run clones at an idle suite boundary."""
import datetime,hashlib,json,pathlib,shutil,subprocess
root=pathlib.Path('/work/field-edge');receipt=root/'e1-local-evidence-receipt.json';manifest=root/'completed-e1-disk-retirement.json'
if manifest.exists() or not receipt.exists() or shutil.disk_usage(root).free>=7*1024**3:raise SystemExit(0)
for p in pathlib.Path('/proc').glob('[0-9]*/cmdline'):
 try:args=p.read_bytes().decode().split('\0')
 except (OSError,UnicodeError):continue
 if pathlib.Path(args[0]).name=='qemu-system-x86_64':assert '-name' in args and args[args.index('-name')+1]=='edge-field-gateway','Only retire at idle boundary'
rows=[json.loads(x) for x in (root/'results.jsonl').read_text().splitlines()];copied=json.loads(receipt.read_text());candidates=[]
for product,lane in [('kuberoot-v0.1.0','release'),('kuberoot-main-1b29543','main'),('k3s-v1.37.2-k3s1','k3s')]:
 row=next(x for x in rows if x.get('scenario_id')=='E1' and x.get('measurement_protocol')=='wan-gateway-conntrack-v2' and x.get('product')==product)
 assert row['status']=='passed' and row['acknowledged_record_loss']==0
 record=next(x for x in copied['records'] if x['product']==product and x['time_utc']==row['time_utc'])
 assert set(record['files'])==set(row['artifact_paths']) and all(pathlib.Path(p).is_file() for p in row['artifact_transfers'].values())
 disk=root/('wan-gateway-v2-'+lane)/'after-fault.qcow2';assert disk.is_file() and not disk.is_symlink();candidates.append((disk,row))
# Read every retained qcow2 header: no candidate may back another disk.
for p in pathlib.Path('/work').rglob('*.qcow2'):
 info=json.loads(subprocess.check_output(['qemu-img','info','--output=json','--force-share',str(p)],text=True))
 assert info.get('full-backing-filename') not in {str(d) for d,_ in candidates},'Disk is a retained backing image'
items=[]
for disk,row in candidates:
 h=hashlib.sha256()
 with disk.open('rb') as f:
  while chunk:=f.read(1024*1024):h.update(chunk)
 items.append({'path':str(disk),'sha256':h.hexdigest(),'allocated_bytes':disk.stat().st_blocks*512,'product':row['product'],'result_utc':row['time_utc']})
r={'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'reason':'Completed successful E1 clones only; all captures/client records copied to Mac and retained in pod. Golden images, failed/invalid disks and firmware are retained. These stopped clone disks are not retained.','local_receipt':str(receipt),'disks':items,'removed':[],'free_before':shutil.disk_usage(root).free}
manifest.write_text(json.dumps(r,indent=2))
for disk,_ in candidates:
 disk.unlink();r['removed'].append(str(disk));manifest.write_text(json.dumps(r,indent=2))
r['free_after']=shutil.disk_usage(root).free;manifest.write_text(json.dumps(r,indent=2));print(json.dumps(r),flush=True)
