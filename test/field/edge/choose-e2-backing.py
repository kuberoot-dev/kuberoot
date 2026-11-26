#!/usr/bin/env python3
"""Reuse only completed immutable evidence; each new delta is fully compared."""
import json,pathlib,sys
source=pathlib.Path(sys.argv[1]);default=sys.argv[2]
result=source.parent/'result.json'
if not source.parent.name.startswith('E2-v2-') or not result.is_file():
 print(default);raise SystemExit(0)
r=json.loads(result.read_text())
if r.get('scenario_id')!='E2' or r.get('fault_phase') in ['tampered','untrusted']:
 print(default);raise SystemExit(0)
for protocol in ['E2-v2','E2']:
 for phase in ['init-crash','pod-network','wrong-content','healthy-control']:
  candidate=source.parent.parent/f"{protocol}-{r['product']}-{phase}-1"
  if candidate==source.parent:continue
  disk=candidate/'failed.qcow2'
  if (candidate/'published').is_file() and disk.is_file() and not (candidate/'system.raw').exists():
   previous=json.loads((candidate/'result.json').read_text())
   assert previous['product']==r['product']
   print(disk);raise SystemExit(0)
print(default)
