#!/usr/bin/env python3
"""Publish only explicitly listed public evidence. Never walk private VM configs."""
import argparse,json,pathlib,subprocess
import importlib.util
_spec=importlib.util.spec_from_file_location("archive_evidence",pathlib.Path(__file__).with_name("archive-evidence.py"));_archive=importlib.util.module_from_spec(_spec);_spec.loader.exec_module(_archive)
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
public=['apt-native-update.txt','storage-upgrade-request.json','storage-upgrade-result.json','storage-upgrade-observations.jsonl','storage-upgrade-commands.txt','storage-upgrade-native.txt','storage-inventory.jsonl','talos-upgrade-progress.txt','credential-replay.json','clone.cast','clone.gif','apt-power-upgrade.txt','members-before.txt','members-after.txt','node-before.json','node-detection.jsonl','api-path-failure.txt','node-api-during-fault.txt','replacement.json','replacement-actions.txt','old-identity-probe.json','members-after.json','transfer-progress.jsonl','transfer-curve.png','bundle-requests.jsonl','upgrade-request-1.json','upgrade-request-2.json','canary.cast','canary.gif','center.cast','center.gif','center-before-reboot.log','rollout-request.json','restricted-proxy.txt','revoked-site-status.json','restore-status.jsonl','serial-redacted.log','live-serial-redacted.log','recovery-curve.png','package-before.txt','package-after.txt','apt-update.txt','apt-install.txt','boot-partitions-before.json','boot-partitions-after.json','talos-before.json','talos-after.json','upgrade-command.txt','churn.jsonl','churn-commands.txt','churn-deployment.json','phase-evidence.json','ca-expiry.json','api-diagnosis.jsonl','guest-clock.txt','guest-http-clock.json','wrong-rtc-serial.log','manual-time-correction.txt','fault-service.log','block-counters.json','fault-epoch','relief-epoch','result.json','observations.jsonl','observer.jsonl','probes.jsonl','records','bootentries-before.json','bootentries-after.json','upgrade-request.json','serial.log','serial.cast','serial.gif','serial-verification.json','recovery.cast','recovery.gif','recovery-verification.json','before-cut-serial.log','isolated-leader-before-reboot.log','isolated-leader-recovery.log','first-heal-agreement.json','final-heal-agreement.json','node-after.json']
if r['scenario_id']=='E10':public += [f'site-{i}-{suffix}' for i in [1,2,3] for suffix in ['observer.jsonl','records']]
for filename in public:
 if r['scenario_id']=='E8' and filename=='serial.log':continue
 f=d/filename
 if not f.exists() and not f.with_name(f.name+'.gz').exists():continue
 target='02-edge/'+('img/' if f.suffix in ['.gif','.png'] else 'raw/')+a.capture+'-'+filename
 if filename=='recovery-curve.png':target='02-edge/img/chart-power-cuts-'+r['product']+'.png'
 if filename=='transfer-curve.png':target='02-edge/img/chart-'+a.capture.removeprefix('t-')+'-transfer.png'
 transfers[target]=str(f)
for filename in ['console.gif','console.png','timing.json']:
 f=d/'vga'/filename
 if f.exists():transfers['02-edge/'+('raw/' if filename.endswith('.json') else 'img/')+a.capture+'-vga-'+filename]=str(f)
transfers=_archive.archive_transfers(transfers)
r['artifact_transfers']=transfers;r['artifact_paths']=list(transfers)
with (root/'results.jsonl').open('a') as f:f.write(json.dumps(r)+'\n')
