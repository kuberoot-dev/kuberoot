#!/usr/bin/env python3
"""Report exact unfinished matrix coverage; never synthesize unsupported rows."""
import argparse,json,pathlib
p=argparse.ArgumentParser();p.add_argument('records',type=pathlib.Path);p.add_argument('--through',choices=['E-B1','E-B2','E-B3']+[f'E{i}' for i in range(1,11)],default='E10');a=p.parse_args();rows=[json.loads(x) for x in a.records.read_text().splitlines() if x.strip()]
products=['kuberoot-v0.1.0','kuberoot-main-1b29543','k3s-v1.37.2-k3s1','talos-v1.14.7'];upgrade=['kuberoot-v0.1.0-testkey',products[1],'k3s-v1.37.1-to-v1.37.2-lab-package',products[3]];checks=[]
def check(scenario,product,n,phase=None,protocol=None):
 candidates=[r for r in rows if r.get('scenario_id')==scenario and r.get('product')==product and (phase is None or r.get('fault_phase')==phase) and (protocol is None or r.get('measurement_protocol')==protocol)]
 valid=[r for r in candidates if not r.get('status','').startswith('invalid')]
 measured=[r for r in valid if r.get('status') not in ['unsupported','not measured']]
 count=len({r.get('repetition',r.get('fault_phase')) for r in measured})
 # A permanent inability must be an explicit recorded finding, not inferred
 # from absent measurements or a future preparation script.
 blocked=[r for r in valid if r.get('status') in ['unsupported','not measured'] and r.get('reason') and (n==1 or r.get('scope')=='all repetitions')]
 checks.append({'scenario_id':scenario,'product':product,'phase':phase,'required':n,'measured':count,'explicit_unavailable':bool(blocked),'complete':count>=n or bool(blocked)})
for product in products:
 check('E-B1',product,1);check('E-B2',product,10,protocol='boot-readiness-v2');check('E-B3',product,1);check('E1',product,1,protocol='wan-gateway-conntrack-v2')
for product in upgrade:
 for phase in ['init-crash','pod-network','wrong-content','incompatible-state','tampered','untrusted']:check('E2',product,10,phase)
 check('E3',product,30)
for product in products:
 for phase in ['100-iops','20-iops','full-90','full-95','full-100','eio']:check('E4',product,1,phase)
 for phase in ['minus-24h','plus-400d','beyond-ca']:check('E6',product,1,phase)
for product in [products[1],products[2]]:check('E5',product,1)
for product in [products[1],products[2],products[3]]:check('E7',product,1)
for phase in ['valid','wrong-key','truncated','s3-down']:check('E8',products[1],1,phase)
check('E8',products[3],1)
for product in [upgrade[0],upgrade[1],upgrade[3]]:check('E9',product,3)
check('E10',products[1],1);check('E10','Talos + Omni',1)
order=['E-B1','E-B2','E-B3']+[f'E{i}' for i in range(1,11)]
checks=[c for c in checks if order.index(c['scenario_id'])<=order.index(a.through)]
missing=[c for c in checks if not c['complete']]
print(json.dumps({'complete_cells':len(checks)-len(missing),'required_cells':len(checks),'missing':missing,'note':'Coverage only. Passing product gates, required kuberoot fixes, capture integrity and PR completion are separate checks.'},indent=2))
raise SystemExit(bool(missing))
