#!/usr/bin/env python3
"""Preserve initial E1 observations and append explicit gateway-fixture audits."""
import datetime,json,pathlib,subprocess
root=pathlib.Path('/work/field-edge');scripts=pathlib.Path(__file__).resolve().parent
all_records=[json.loads(x) for x in (root/'results.jsonl').read_text().splitlines()]
for product in ['kuberoot-v0.1.0','kuberoot-main-1b29543','k3s-v1.37.2-k3s1']:
 d=root/'results'/('E1-'+product)
 if (d/'fixture-audit.json').exists():continue
 if (d/'result.json').exists():r=json.loads((d/'result.json').read_text());r['supersedes_result_time_utc']=r['time_utc']
 else:
  baseline=next(x for x in all_records if x.get('scenario_id')=='E-B1' and x.get('product')==product)
  metrics=json.loads(subprocess.check_output(['python3',str(scripts/'analyse-observer.py'),str(d/'observer.jsonl'),'--fault-epoch',(d/'power-cut-epoch').read_text().strip()],text=True))
  check=subprocess.run(['python3',str(scripts/'check-records.py'),str(d/'observer.jsonl'),str(d/'records')],capture_output=True,text=True)
  metrics.update(json.loads(check.stdout));metrics['full_hour_completed']=False
  subprocess.run(['python3',str(scripts/'render-serial.py'),str(d/'recovery.cast')],check=True)
  r={'scenario_id':'E1','product':product,'image_digest':baseline['image_digest'],'source_commit':baseline['source_commit'],'vcpu':2,'ram_mib':4096,'topology':'single multicast LAN; 512 MiB Debian gateway with exhausted conntrack table','fault_seed':None,'fault_phase':'partial WAN outage; hard reset at minute 10; interrupted after fixture diagnosis','outage_seconds':None,'recovery_seconds':metrics.get('recovery_seconds_first_60s_sustained_success'),'acknowledged_record_loss':metrics.get('acknowledged_record_loss'),'manual_actions':['Interrupted the partial measurement after gateway conntrack exhaustion was proven'],'metrics':metrics,'artifact_transfers':{}}
  capture='t-E1-'+product
  for ext in ['gif','png','txt','cast','timing.json']:
   r['artifact_transfers']['02-edge/'+('img/' if ext in ['gif','png'] else 'raw/')+capture+'.'+ext]=str(root/'captures'/(capture+'.'+ext))
  for name in ['observer.jsonl','interruption.json','before-cut-serial.log','recovery-serial.log','tcp-inbound-only.jsonl','tcp-handshakes.jsonl','tcp-handshake-analysis.json']:
   if (d/name).exists():r['artifact_transfers']['02-edge/raw/'+capture+'-'+name]=str(d/name)
  r['artifact_transfers']['02-edge/img/'+capture+'-serial.gif']=str(d/'recovery.gif')
 r.update(status='invalid-fixture',record_kind='fixture-audit',measurement_protocol='wan-gateway-v1-invalid',reason='Gateway nf_conntrack_max=4096 exhausted; kernel explicitly dropped packets. Preserve the observed errors, but do not attribute this run to a DUT defect. Repeat all products with the same corrected gateway.',time_utc=datetime.datetime.now(datetime.timezone.utc).isoformat())
 r['metrics']['gateway_conntrack_limit']=4096
 transfers=r['artifact_transfers'];transfers['02-edge/raw/E1-gateway-conntrack-exhaustion.txt']=str(root/'gateway-conntrack-exhaustion.txt')
 capture='t-E1-gateway-conntrack-fix'
 for ext in ['gif','png','txt','cast','timing.json']:
  transfers['02-edge/'+('img/' if ext in ['gif','png'] else 'raw/')+capture+'.'+ext]=str(root/'captures'/(capture+'.'+ext))
 r['artifact_paths']=list(transfers);(d/'fixture-audit.json').write_text(json.dumps(r,indent=2))
 with (root/'results.jsonl').open('a') as f:f.write(json.dumps(r)+'\n')
 print(json.dumps({'product':product,'status':r['status'],'retained_source_observations':True}),flush=True)
