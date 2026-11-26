#!/usr/bin/env python3
"""Derive recovery from observed responses, not Node Ready or service status."""
import argparse,datetime,json,pathlib
p=argparse.ArgumentParser();p.add_argument('observer',type=pathlib.Path);p.add_argument('--fault-epoch',type=float,required=True);a=p.parse_args()
rows=[json.loads(line) for line in a.observer.read_text().splitlines()]
for r in rows:
    r['epoch']=datetime.datetime.fromisoformat(r['utc']).timestamp() if 'utc' in r else None
service=sorted([r for r in rows if r['kind'] in ['http','append'] and r['epoch'] is not None],key=lambda r:r['start_seconds'])
after=[r for r in service if r['epoch']>=a.fault_epoch]
run_start=None; previous=None; recovered=None
for r in after:
    if not r.get('ok') or (previous is not None and r['start_seconds']-previous>.2):
        run_start=None
    elif run_start is None: run_start=r
    if run_start and r['start_seconds']-run_start['start_seconds']>=60:
        recovered=run_start['epoch']-a.fault_epoch;break
    previous=r['start_seconds']
result={'recovery_seconds_first_60s_sustained_success':recovered,
 'service_requests':len(service),'service_failed':sum(not r.get('ok',False) for r in service),
 'acknowledged_records':sum(r.get('acknowledged',False) for r in rows),
 'driver_overloads':sum(r.get('error')=='driver_overload' for r in rows),
 'api_probes':sum(r['kind']=='api' for r in rows),'api_failures':sum(r['kind']=='api' and not r.get('ok',False) for r in rows),
 'scope':'HTTP/append client observations; durability requires check-records.py separately'}
print(json.dumps(result,indent=2))
