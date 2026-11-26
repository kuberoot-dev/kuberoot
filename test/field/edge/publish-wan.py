#!/usr/bin/env python3
"""Evaluate E1 against real observer records, including the pre-reset WAN outage."""
import datetime,json,os,pathlib
root=pathlib.Path('/work/field-edge');product=os.environ['PRODUCT'];name=os.environ['RUN_NAME'];d=root/'results'/name
metrics=json.loads((d/'recovery.json').read_text());metrics.update(json.loads((d/'durability.json').read_text()))
fault=float((d/'fault-epoch').read_text());cut=float((d/'power-cut-epoch').read_text())
rows=[json.loads(x) for x in (d/'observer.jsonl').read_text().splitlines()]
local=[r for r in rows if r['kind'] in ['http','append'] and r.get('utc') and fault<=datetime.datetime.fromisoformat(r['utc']).timestamp()<cut-2]
metrics['wan_blackhole_before_reset_requests']=len(local);metrics['wan_blackhole_before_reset_failures']=sum(not r.get('ok') for r in local)
missing=json.loads((d/'missing-image.json').read_text());states=[c.get('state',{}).get('waiting',{}) for c in missing.get('status',{}).get('containerStatuses',[])]
metrics['missing_image_reasons']=[s.get('reason') for s in states]
errors=[]
if not local or metrics['wan_blackhole_before_reset_failures']:errors.append('local requests failed before the hard reset')
r=metrics.get('recovery_seconds_first_60s_sustained_success')
if r is None or r>180:errors.append('W did not recover within 180 seconds')
if metrics['acknowledged_record_loss'] or metrics['corrupt_records']:errors.append('acknowledged records missing or corrupt')
if not any(x in ['ErrImagePull','ImagePullBackOff'] for x in metrics['missing_image_reasons']):errors.append('missing-image failure not explicit')
if metrics['driver_overloads']:errors.append('observer dispatch overloaded; measurements invalid')
transfers={};capture='t-'+name
for ext in ['gif','png','txt','cast','timing.json']:
 transfers['02-edge/'+('img/' if ext in ['gif','png'] else 'raw/')+capture+'.'+ext]=str(root/'captures'/(capture+'.'+ext))
for filename in ['observer.jsonl','durability.json','recovery.json','missing-image.json','before-cut-serial.log','recovery-serial.log','fault-epoch','power-cut-epoch']:
 transfers['02-edge/raw/'+name+'-'+filename]=str(d/filename)
transfers['02-edge/img/'+capture+'-serial.gif']=str(d/'recovery.gif');transfers['02-edge/raw/'+name+'-serial.cast']=str(d/'recovery.cast')
record={'scenario_id':'E1','product':product,'image_digest':os.environ['IMAGE_DIGEST'],'source_commit':os.environ['SOURCE_COMMIT'],'vcpu':2,'ram_mib':4096,'topology':'single multicast LAN; gateway WAN/DNS blackhole; local observer in gateway','fault_seed':None,'fault_phase':'60 min WAN blackhole; power cut at minute 10','outage_seconds':None,'acknowledged_record_loss':metrics['acknowledged_record_loss'],'recovery_seconds':r,'manual_actions':[],'metrics':metrics,'status':'failed' if errors else 'passed','reason':'; '.join(errors) if errors else 'all E1 service, recovery, durability and missing-image gates passed','time_utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'artifact_transfers':transfers,'artifact_paths':list(transfers)}
(d/'result.json').write_text(json.dumps(record,indent=2))
with (root/'results.jsonl').open('a') as f:f.write(json.dumps(record)+'\n')
