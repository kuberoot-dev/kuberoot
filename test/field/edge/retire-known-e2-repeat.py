#!/usr/bin/env python3
"""Retire only a stopped, fully published repetition of an already kept finding.
First case disks, novel failures and all backend-invalid attempts remain intact.
Public captures, complete client records and measurements are never removed.
"""
import argparse,datetime,json,pathlib
p=argparse.ArgumentParser();p.add_argument('disk',type=pathlib.Path);p.add_argument('--dry-run',action='store_true');a=p.parse_args();disk=a.disk
root=pathlib.Path('/work/field-edge');d=disk.parent
assert disk.name=='system.raw' and disk.resolve().is_relative_to(root/'results') and not disk.is_symlink()
if not d.name.startswith('E2-v2-') or not (d/'result.json').is_file():raise SystemExit(1)
r=json.loads((d/'result.json').read_text());m=r.get('metrics',{});rep=r.get('repetition',0)
if not (r.get('measurement_protocol')=='upgrade-client-disconnect-v2' and r.get('scenario_id')=='E2' and rep>1 and r.get('status')=='failed' and m.get('fault_verified') and not m.get('unexpected_block_io_failures') and not m.get('driver_overloads')):raise SystemExit(1)
first=d.with_name('E2-v2-'+r['product']+'-'+r['fault_phase']+'-1')
if not all((first/n).is_file() for n in ['result.json','published','failed.qcow2']):raise SystemExit(1)
f=json.loads((first/'result.json').read_text());fm=f.get('metrics',{})
keys=['status','reason','source_commit','image_digest','product','fault_phase','measurement_protocol','acknowledged_record_loss','manual_actions']
metric_keys=['target_bundle_sha256','target_binary_sha256','target_version','fault_verified','faulty_release_marked_good','corrupt_records','duplicate_records','missing_ids','final_60s_http_failures','final_60s_append_failures','final_60s_api_failures']
if any(r.get(k)!=f.get(k) for k in keys) or any(m.get(k)!=fm.get(k) for k in metric_keys):raise SystemExit(1)
for path in pathlib.Path('/proc').glob('[0-9]*/cmdline'):
 try:argv=path.read_bytes().decode().split('\0')
 except (OSError,UnicodeError):continue
 if argv[0].endswith('qemu-system-x86_64') and any(str(disk) in x for x in argv):raise SystemExit('Refusing to retire an attached disk')
rows=[json.loads(l) for l in (root/'results.jsonl').read_text().splitlines()]
record=next((x for x in rows if x.get('product')==r['product'] and x.get('time_utc')==r['time_utc']),None)
assert record and record.get('artifact_transfers') and all(pathlib.Path(s).is_file() for s in record['artifact_transfers'].values()),'Publish all evidence before retiring a repeat'
manifest={'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'source':str(disk),'allocated_bytes':disk.stat().st_blocks*512,'retained_first_finding_disk':str(first/'failed.qcow2'),'first_result_utc':f['time_utc'],'result_utc':r['time_utc'],'status':r['status'],'reason':'Later identical observed E2 failure; first full disk, all public captures/client records and every novel/invalid state retained. This repeat disk is not retained.','acknowledged_record_loss':r.get('acknowledged_record_loss'),'unknown_integrity_stays_unknown':r.get('acknowledged_record_loss') is None,'dry_run':a.dry_run}
print(json.dumps(manifest),flush=True)
if not a.dry_run:
 (d/'known-repeat-retirement.json').write_text(json.dumps(manifest,indent=2));disk.unlink()
