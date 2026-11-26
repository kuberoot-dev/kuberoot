#!/usr/bin/env python3
"""Lightweight Mac-side artifact copier and checkpoint publisher; no VM/builds."""
import json,os,pathlib,re,subprocess,time
root=pathlib.Path('/Users/tym83/projects/kuberoot-articles')
scripts=pathlib.Path(__file__).resolve().parent
env=dict(os.environ,KUBECONFIG='/Users/tym83/workshop.txt')
base=['kubectl','--context','admin@workshop','-n','tenant-kuberoot']
def remote(*args):return subprocess.run(base+['exec','kr-edge-stand','--',*args],env=env,text=True,capture_output=True,timeout=90)
def copy(src,dst):
 subprocess.run(base+['cp','kr-edge-stand:'+src,str(dst)],env=env,check=True,capture_output=True,timeout=90)
while True:
 try:
  existing=[json.loads(x) for x in (root/'results/edge.jsonl').read_text().splitlines() if x.strip()]
  known={(x.get('scenario_id'),x.get('product'),x.get('time_utc')) for x in existing}
  output=remote('cat','/work/field-edge/results.jsonl')
  if output.returncode:
   time.sleep(20);continue
  for line in output.stdout.splitlines():
   r=json.loads(line)
   if (r.get('scenario_id'),r.get('product'),r.get('time_utc')) in known:continue
   if r.get('scenario_id')!='E-B1':continue
   sample=next(pathlib.Path(x).name for x in r['artifact_paths'] if x.endswith('-samples.jsonl'))
   stem=sample.removesuffix('-samples.jsonl')
   assert re.fullmatch(r'[A-Za-z0-9_.-]+',stem)
   source='/work/field-edge/results/'+stem
   for artifact in r['artifact_paths']:
    destination=root/artifact
    destination.parent.mkdir(parents=True,exist_ok=True)
    origin=source+'/samples.jsonl' if artifact.endswith('-samples.jsonl') else '/work/field-edge/captures/'+destination.name
    copy(origin,destination)
   for name in ['inventory','node','result']:
    copy(source+'/'+name+'.json',root/'02-edge/raw'/f'{stem}-{name}.json')
   chart='chart-idle-'+stem.removeprefix('E-B1-')+'.png'
   outcome=remote('python3','/work/field-edge/scripts/chart-baseline.py',source+'/samples.jsonl','/work/field-edge/captures/'+chart,r['product']+' idle memory')
   if outcome.returncode:raise RuntimeError(outcome.stderr)
   copy('/work/field-edge/captures/'+chart,root/'02-edge/img'/chart)
   r['artifact_paths'].append('02-edge/img/'+chart)
   # Every scene is rendered and copied before the result is published.
   record=root/'02-edge/raw'/f'{stem}-published-result.json'
   record.write_text(json.dumps(r,indent=2)+'\n')
   subprocess.run(['python3',str(scripts/'update-results.py'),'--article-root',str(root),'--record',str(record)],check=True)
   with (root/'02-edge/ARTIFACTS.md').open('a') as f:
    f.write(f"\n### {r['scenario_id']} — {r['product']}\n\n")
    f.write(f"Source `{r['source_commit']}`, image `{r['image_digest']}`. {r['samples_ok']}/{r['samples_total']} samples; status {r['status']}.\n\n")
    for artifact in r['artifact_paths']:
     kind='Измеренная временная кривая idle RAM' if pathlib.Path(artifact).name.startswith('chart-') else 'Реальный запуск и вывод E-B1' if artifact.endswith(('.gif','.png','.txt')) else 'Исходные samples E-B1'
     f.write(f'- `{artifact}` — {kind}.\n')
    f.write('\nMetrics: `'+json.dumps(r['metrics'],ensure_ascii=False)+'`.\n')
   print(json.dumps({'published':r['product'],'status':r['status']}),flush=True)
 except Exception as exc:
  print('sync error: '+str(exc),flush=True)
 time.sleep(20)
