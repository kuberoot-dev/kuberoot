#!/usr/bin/env python3
"""Continue the full table only after the original main v2 lane really ended."""
import argparse,datetime,json,pathlib,subprocess,time
p=argparse.ArgumentParser();p.add_argument('--parent',required=True,type=int);a=p.parse_args()
root=pathlib.Path('/work/field-edge');scripts=pathlib.Path(__file__).resolve().parent
stat=pathlib.Path(f'/proc/{a.parent}/stat')
def process():
 try:
  fields=stat.read_text().rsplit(')',1)[1].split()
  return fields[0],fields[19] # state and starttime, robust to spaces in comm
 except FileNotFoundError:return None
initial=process();assert initial and initial[0]!='Z','Main runner must still be active at watcher creation'
print(datetime.datetime.now(datetime.timezone.utc).isoformat(), 'waiting for main runner',a.parent,flush=True)
while True:
 current=process()
 if not current or current[0]=='Z' or current[1]!=initial[1]:break
 time.sleep(20)
rows=[json.loads(line) for line in (root/'results.jsonl').read_text().splitlines()]
for phase in ['init-crash','pod-network','wrong-content','incompatible-state','tampered','untrusted']:
 repetitions={r.get('repetition') for r in rows if r.get('scenario_id')=='E2' and r.get('measurement_protocol')=='upgrade-client-disconnect-v2' and r.get('product')=='kuberoot-main-1b29543' and r.get('fault_phase')==phase and r.get('status') in ['passed','failed']}
 assert set(range(1,11))<=repetitions,'Main lane ended incomplete; diagnose before continuation: '+phase
print(datetime.datetime.now(datetime.timezone.utc).isoformat(), 'main v2 complete; continuing table',flush=True)
subprocess.run(['bash',str(scripts/'run-table-tail.sh')],check=True)
