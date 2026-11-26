#!/usr/bin/env python3
"""Publish only explicitly listed public evidence. Never walk private VM configs."""
import argparse,json,pathlib,subprocess
p=argparse.ArgumentParser();p.add_argument('directory',type=pathlib.Path);p.add_argument('capture');a=p.parse_args()
root=pathlib.Path('/work/field-edge');scripts=pathlib.Path(__file__).resolve().parent;d=a.directory
assert d.is_relative_to(root) and a.capture.startswith('t-') and '/' not in a.capture
r=json.loads((d/'result.json').read_text());transfers={}
for ext in ['gif','png','txt','cast','timing.json']:
 f=root/'captures'/(a.capture+'.'+ext);assert f.is_file(),str(f)
 transfers['02-edge/'+('img/' if ext in ['gif','png'] else 'raw/')+f.name]=str(f)
verified=root/'captures'/(a.capture+'-verification.json')
subprocess.run(['python3',str(scripts/'verify-capture.py'),str(root/'captures'/a.capture)],check=True,stdout=verified.open('w'))
transfers['02-edge/raw/'+verified.name]=str(verified)
public=['churn.jsonl','churn-commands.txt','churn-deployment.json','phase-evidence.json','ca-expiry.json','api-diagnosis.jsonl','guest-clock.txt','guest-http-clock.json','wrong-rtc-serial.log','manual-time-correction.txt','fault-service.log','block-counters.json','fault-epoch','relief-epoch','result.json','observations.jsonl','observer.jsonl','probes.jsonl','records','bootentries-before.json','bootentries-after.json','upgrade-request.json','serial.log','serial.cast','serial.gif','serial-verification.json','recovery.cast','recovery.gif','recovery-verification.json','before-cut-serial.log','isolated-leader-before-reboot.log','isolated-leader-recovery.log','first-heal-agreement.json','final-heal-agreement.json','node-after.json']
for filename in public:
 f=d/filename
 if not f.exists():continue
 target='02-edge/'+('img/' if f.suffix in ['.gif','.png'] else 'raw/')+a.capture+'-'+filename
 transfers[target]=str(f)
for filename in ['console.gif','console.png','timing.json']:
 f=d/'vga'/filename
 if f.exists():transfers['02-edge/'+('raw/' if filename.endswith('.json') else 'img/')+a.capture+'-vga-'+filename]=str(f)
r['artifact_transfers']=transfers;r['artifact_paths']=list(transfers)
with (root/'results.jsonl').open('a') as f:f.write(json.dumps(r)+'\n')
