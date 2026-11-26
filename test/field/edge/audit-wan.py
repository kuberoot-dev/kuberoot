#!/usr/bin/env python3
"""Recheck the entire blackhole window and correlate sampled fabric drops.
This produces supplementary evidence; it never overwrites a recorded run.
"""
import argparse,datetime,json,pathlib,collections
p=argparse.ArgumentParser();p.add_argument('directory',type=pathlib.Path);p.add_argument('--monitor',type=pathlib.Path,default=pathlib.Path('/work/field-edge/fabric-monitor.jsonl'));a=p.parse_args();d=a.directory
fault=float((d/'fault-epoch').read_text());cut=float((d/'power-cut-epoch').read_text());original=json.loads((d/'result.json').read_text());recovery=original['recovery_seconds'];end=fault+3600
rows=[json.loads(x) for x in (d/'observer.jsonl').read_text().splitlines()]
def epoch(r):return datetime.datetime.fromisoformat(r['utc']).timestamp()
service=[r for r in rows if r['kind'] in ['http','append'] and r.get('utc')]
pre=[r for r in service if fault<=epoch(r) and epoch(r)+r.get('latency_seconds',0)<cut]
post=[r for r in service if recovery is not None and cut+recovery+60<=epoch(r)<end]
monitor=[json.loads(x) for x in a.monitor.read_text().splitlines()] if a.monitor.exists() else []
drops=[]
for before,after in zip(monitor,monitor[1:]):
 delta=after['network_namespace_counters']['Udp']['RcvbufErrors']-before['network_namespace_counters']['Udp']['RcvbufErrors']
 if delta>0 and epoch(after)>=fault and epoch(before)<=end:
  failures=[r for r in service if not r.get('ok') and epoch(before)-2<=epoch(r)<=epoch(after)]
  drops.append({'from_utc':before['utc'],'to_utc':after['utc'],'new_udp_receive_buffer_errors':delta,'overlapping_failed_requests':len(failures)})
failures=[{k:r.get(k) for k in ['kind','id','utc','latency_seconds','error','status']} for r in pre+post if not r.get('ok')]
r={'scenario_id':'E1','product':original['product'],'kind':'supplementary-whole-window-audit','original_status':original['status'],'before_reset':{'requests':len(pre),'failed':sum(not r.get('ok') for r in pre)},'after_sustained_recovery_until_blackhole_end':{'requests':len(post),'failed':sum(not r.get('ok') for r in post)},'failures':failures,'udp_drop_intervals':drops,'monitor_coverage_utc':[monitor[0]['utc'],monitor[-1]['utc']] if monitor else [],'caveat':'Counter correlation is not causal proof. Monitor coverage may begin after the fault. Recovery requests and requests overlapping the power cut are excluded from continuity gates.'}
(d/'whole-window-audit.json').write_text(json.dumps(r,indent=2))
print(json.dumps({k:v for k,v in r.items() if k!='failures'},indent=2))
