#!/usr/bin/env python3
"""Evaluate E1 against real observer records, including the pre-reset WAN outage."""
import datetime,json,os,pathlib,subprocess
import importlib.util
_spec=importlib.util.spec_from_file_location("archive_evidence",pathlib.Path(__file__).with_name("archive-evidence.py"));_archive=importlib.util.module_from_spec(_spec);_spec.loader.exec_module(_archive)
root=pathlib.Path('/work/field-edge');product=os.environ['PRODUCT'];name=os.environ['RUN_NAME'];d=root/'results'/name
metrics=json.loads((d/'recovery.json').read_text());metrics.update(json.loads((d/'durability.json').read_text()))
fault=float((d/'fault-epoch').read_text());cut=float((d/'power-cut-epoch').read_text())
rows=[json.loads(x) for x in ((d/'observer.jsonl').read_text() if (d/'observer.jsonl').exists() else __import__('gzip').open(d/'observer.jsonl.gz','rt').read()).splitlines()]
local=[r for r in rows if r['kind'] in ['http','append'] and r.get('utc') and fault<=datetime.datetime.fromisoformat(r['utc']).timestamp()<cut-2]
metrics['wan_blackhole_before_reset_requests']=len(local);metrics['wan_blackhole_before_reset_failures']=sum(not r.get('ok') for r in local)
missing=json.loads((d/'missing-image.json').read_text());states=[c.get('state',{}).get('waiting',{}) for c in missing.get('status',{}).get('containerStatuses',[])]
metrics['missing_image_reasons']=[s.get('reason') for s in states]
monitor=[json.loads(x) for x in ((d/'gateway-monitor.jsonl').read_text() if (d/'gateway-monitor.jsonl').exists() else __import__('gzip').open(d/'gateway-monitor.jsonl.gz','rt').read()).splitlines()]
metrics['gateway_conntrack_max']=min(x['conntrack']['max'] for x in monitor)
metrics['gateway_conntrack_count_peak']=max(x['conntrack']['count'] for x in monitor)
metrics['gateway_conntrack_drop_delta']=monitor[-1]['conntrack']['drop']-monitor[0]['conntrack']['drop']
metrics['gateway_monitor_samples']=len(monitor)
errors=[]
fixture_invalid=metrics['gateway_conntrack_drop_delta']!=0 or metrics['gateway_conntrack_max']<65536
if fixture_invalid:errors.append('gateway conntrack fixture invalid')
if not local or metrics['wan_blackhole_before_reset_failures']:errors.append('local requests failed before the hard reset')
r=metrics.get('recovery_seconds_first_60s_sustained_success')
if r is None or r>180:errors.append('W did not recover within 180 seconds')
post=[x for x in rows if x['kind'] in ['http','append'] and x.get('utc') and r is not None and cut+r+60<=datetime.datetime.fromisoformat(x['utc']).timestamp()<fault+3600]
metrics['wan_blackhole_after_recovery_requests']=len(post);metrics['wan_blackhole_after_recovery_failures']=sum(not x.get('ok') for x in post)
if not post or metrics['wan_blackhole_after_recovery_failures']:errors.append('local requests failed after sustained recovery during WAN blackhole')
if metrics['acknowledged_record_loss'] or metrics['corrupt_records']:errors.append('acknowledged records missing or corrupt')
if not any(x in ['ErrImagePull','ImagePullBackOff'] for x in metrics['missing_image_reasons']):errors.append('missing-image failure not explicit')
if metrics['driver_overloads']:errors.append('observer dispatch overloaded; measurements invalid')
transfers={};capture='t-'+name
for ext in ['gif','png','txt','cast','timing.json']:
 transfers['02-edge/'+('img/' if ext in ['gif','png'] else 'raw/')+capture+'.'+ext]=str(root/'captures'/(capture+'.'+ext))
for filename in ['recovered-records','gateway-monitor.jsonl','observer.jsonl','durability.json','recovery.json','missing-image.json','before-cut-serial.log','recovery-serial.log','fault-epoch','power-cut-epoch']:
 transfers['02-edge/raw/'+name+'-'+filename]=str(d/filename)
transfers['02-edge/img/'+capture+'-serial.gif']=str(d/'recovery.gif');transfers['02-edge/raw/'+name+'-serial.cast']=str(d/'recovery.cast')
if (d/'vga/console.gif').exists():
 transfers['02-edge/img/'+capture+'-vga.gif']=str(d/'vga/console.gif')
 transfers['02-edge/img/'+capture+'-vga.png']=str(d/'vga/console.png')
 transfers['02-edge/raw/'+name+'-vga-timing.json']=str(d/'vga/timing.json')
record={'measurement_protocol':'wan-gateway-conntrack-v2','scenario_id':'E1','product':product,'image_digest':os.environ['IMAGE_DIGEST'],'source_commit':os.environ['SOURCE_COMMIT'],'vcpu':2,'ram_mib':4096,'topology':'single multicast LAN; gateway WAN/DNS blackhole; local observer in gateway','fault_seed':None,'fault_phase':'60 min WAN blackhole; power cut at minute 10','outage_seconds':None,'acknowledged_record_loss':metrics['acknowledged_record_loss'],'recovery_seconds':r,'manual_actions':[],'metrics':metrics,'status':'invalid-fixture' if fixture_invalid else 'failed' if errors else 'passed','reason':'; '.join(errors) if errors else 'all E1 service, recovery, durability and missing-image gates passed','time_utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'artifact_transfers':transfers,'artifact_paths':list(transfers)}
(d/'result.json').write_text(json.dumps(record,indent=2))
subprocess.run(['python3',str(pathlib.Path(__file__).resolve().parent/'audit-wan.py'),str(d)],check=True)
transfers['02-edge/raw/'+name+'-whole-window-audit.json']=str(d/'whole-window-audit.json')
monitor=root/'fabric-monitor.jsonl'
if monitor.exists():
 if not (d/'fabric-monitor-snapshot.jsonl').exists() and not (d/'fabric-monitor-snapshot.jsonl.gz').exists():(d/'fabric-monitor-snapshot.jsonl').write_bytes(monitor.read_bytes())
 transfers['02-edge/raw/'+name+'-fabric-monitor.jsonl']=str(d/'fabric-monitor-snapshot.jsonl')
subprocess.run(['python3',str(pathlib.Path(__file__).resolve().parent/'chart-wan.py'),str(d)],check=True)
transfers['02-edge/img/chart-'+name+'.png']=str(d/'wan-curve.png')
comparison='chart-wan-'+os.environ.get('WAN_RUN_LABEL','gateway-v2')+'-recovery.png'
if (root/'captures'/comparison).exists():transfers['02-edge/img/'+comparison]=str(root/'captures'/comparison)
transfers=_archive.archive_transfers(transfers)
record['artifact_transfers']=transfers
record['artifact_paths']=list(transfers)
(d/'result.json').write_text(json.dumps(record,indent=2))
with (root/'results.jsonl').open('a') as f:f.write(json.dumps(record)+'\n')
