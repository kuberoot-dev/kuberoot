#!/usr/bin/env python3
"""Bounded repeat ONLY for a proved backend fault before the intended B fault.
Every invalid attempt is published and its disk retained. Product failures,
postboot errors, signature tests and other fixture failures are never retried here.
"""
import datetime,json,pathlib,re,sys
p=pathlib.Path(sys.argv[1]);base=pathlib.Path(sys.argv[2]);assert p.parent==base.parent and p.is_relative_to('/work/field-edge/results')
r=json.loads((p/'result.json').read_text());m=r['metrics']
assert r['status']=='invalid-fixture' and r['fault_phase'] in ['init-crash','pod-network','wrong-content','incompatible-state']
assert m.get('preboot_failure_message') and m.get('unexpected_block_io_failures',0)>0
assert m.get('target_boot_observed') is False and m.get('fault_verified') is False
assert (p/'failed.qcow2').is_file() and not (p/'system.raw').exists()
rows=[json.loads(x) for x in (p.parent.parent/'results.jsonl').read_text().splitlines()]
assert any(x.get('time_utc')==r['time_utc'] and x.get('product')==r['product'] for x in rows)
match=re.fullmatch(re.escape(base.name)+r'(?:-attempt([2-9][0-9]*))?',p.name);assert match
attempt=int(match[1] or 1)
if attempt>=3:raise SystemExit('Three backend-invalid attempts retained; further diagnosis required before this case')
next_name=base.name+'-attempt'+str(attempt+1);assert not p.with_name(next_name).exists()
review={'directory':next_name,'reason':'Automatic bounded repeat of proved preboot backend failure; intended B fault not reached. Same raw/cache=none image/fabric; no guest repair; all invalid evidence retained.','utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'previous_result_utc':r['time_utc'],'preboot_failure_message':m['preboot_failure_message'],'unexpected_block_io_failures':m['unexpected_block_io_failures'],'maximum_total_attempts':3}
(p/'retry.json').write_text(json.dumps(review,indent=2));(p/'published').touch();print(json.dumps(review),flush=True)
