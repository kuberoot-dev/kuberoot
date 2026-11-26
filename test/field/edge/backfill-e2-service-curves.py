#!/usr/bin/env python3
"""Supplement already published E2 runs without changing their measurements."""
import json,pathlib,subprocess
root=pathlib.Path('/work/field-edge');scripts=pathlib.Path(__file__).resolve().parent;items=[]
for r in [json.loads(x) for x in (root/'results.jsonl').read_text().splitlines()]:
 if r.get('scenario_id')!='E2' or r.get('measurement_protocol')!='upgrade-client-disconnect-v2' or r.get('status') not in ['passed','failed']:continue
 sources=[pathlib.Path(v) for k,v in r.get('artifact_transfers',{}).items() if k.endswith('-result.json') and v.endswith('/result.json')]
 assert len(sources)==1
 d=sources[0].parent;target='02-edge/img/chart-'+d.name+'-service.png'
 if target in r['artifact_transfers']:continue
 subprocess.run(['python3',str(scripts/'chart-upgrade-trial.py'),str(d)],check=True)
 if not (d/'service-curve.png').exists():continue
 items.append({'scenario_id':'E2','product':r['product'],'fault_phase':r['fault_phase'],'repetition':r['repetition'],'source_commit':r['source_commit'],'result_time_utc':r['time_utc'],'transfers':{target:str(d/'service-curve.png'),'02-edge/raw/t-'+d.name+'-service-curve-provenance.json':str(d/'service-curve-provenance.json')}})
(root/'e2-service-curve-supplement.json').write_text(json.dumps(items,indent=2));print('Supplemental charts: '+str(len(items)))
