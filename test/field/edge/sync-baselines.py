#!/usr/bin/env python3
"""Lightweight Mac-side artifact copier and checkpoint publisher; no VM/builds."""
import json,os,pathlib,re,subprocess,time,tarfile,shutil
root=pathlib.Path('/Users/tym83/projects/kuberoot-articles')
scripts=pathlib.Path(__file__).resolve().parent
env=dict(os.environ,KUBECONFIG='/Users/tym83/workshop.txt')
base=['kubectl','--context','admin@workshop','-n','tenant-kuberoot']
def remote(*args):return subprocess.run(base+['exec','kr-edge-stand','--',*args],env=env,text=True,capture_output=True,timeout=90)
def copy(src,dst):
 temporary=dst.with_name(dst.name+'.partial')
 subprocess.run(base+['cp','kr-edge-stand:'+src,str(temporary)],env=env,check=True,capture_output=True,timeout=90)
 temporary.replace(dst)
def copy_many(transfers):
 origins={origin:root/destination for destination,origin in transfers.items()}
 assert all(str(dst).startswith(str(root/'02-edge')+'/') and '..' not in str(dst) for dst in origins.values())
 assert all(src.startswith('/work/field-edge/') for src in origins)
 process=subprocess.Popen(base+['exec','kr-edge-stand','--','tar','-cf','-','--',*origins],env=env,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL)
 received=set()
 try:
  with tarfile.open(fileobj=process.stdout,mode='r|') as archive:
   for member in archive:
    origin='/'+member.name.lstrip('/');assert member.isfile() and origin in origins
    destination=origins[origin];temporary=destination.with_name(destination.name+'.partial')
    with temporary.open('wb') as out:shutil.copyfileobj(archive.extractfile(member),out,1024*1024)
    assert temporary.stat().st_size==member.size
    temporary.replace(destination);received.add(origin)
  assert process.wait(timeout=30)==0 and received==set(origins), 'incomplete artifact stream'
 except BaseException:
  process.kill();process.wait();raise
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
   if r.get('artifact_transfers'):
    copy_many(r['artifact_transfers'])
    stamp=re.sub(r'[^0-9A-Za-z]', '-', r.get('time_utc','unknown'))
    stem=f"{r['scenario_id']}-{r['product']}-{r.get('repetition','run')}-{stamp}"
    record=root/'02-edge/raw'/f'{stem}-published-result.json'
    record.write_text(json.dumps(r,indent=2)+'\n')
    subprocess.run(['python3',str(scripts/'update-results.py'),'--article-root',str(root),'--record',str(record)],check=True)
    with (root/'02-edge/ARTIFACTS.md').open('a') as f:
     f.write(f"\n### {r['scenario_id']} — {r['product']}, repetition {r.get('repetition','—')}\n\n")
     f.write(f"Source `{r.get('source_commit')}`, image `{r.get('image_digest')}`. Status {r['status']}.\n\n")
     for artifact in r['artifact_paths']:
      description='Реальная serial-консоль загрузки' if '-serial.' in artifact else 'Реальный запуск, состояние терминала или исходный вывод'
      f.write(f"- `{artifact}` — {r['scenario_id']}: {description}.\n")
     f.write('\nMetrics: `'+json.dumps(r.get('metrics',{}),ensure_ascii=False)+'`.\n')
    print(json.dumps({'published':stem,'status':r['status']}),flush=True)
    continue
   if r.get('status') in ['not measured','unsupported'] and not r.get('artifact_paths'):
    stamp=re.sub(r'[^0-9A-Za-z]', '-',r.get('time_utc','unknown'));product=re.sub(r'[^0-9A-Za-z-]','-',r['product'])
    record=root/'02-edge/raw'/f'{r["scenario_id"]}-{product}-{stamp}-published-result.json';record.write_text(json.dumps(r,indent=2)+'\n')
    subprocess.run(['python3',str(scripts/'update-results.py'),'--article-root',str(root),'--record',str(record)],check=True)
    print(json.dumps({'published':r['product'],'status':r['status']}),flush=True);continue
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
   vm='vm-1' if r['product']=='kuberoot-v0.1.0' else 'vm-main' if r['product'].startswith('kuberoot-main') else 'k3s' if r['product'].startswith('k3s') else 'talos'
   outcome=remote('python3','/work/field-edge/scripts/collect-footprint.py',r['product'],'/work/field-edge/'+vm,source+'/footprint.json')
   if outcome.returncode:raise RuntimeError(outcome.stderr)
   footprint=json.loads(outcome.stdout)
   footprint_path='02-edge/raw/'+stem+'-footprint.json'
   copy(source+'/footprint.json',root/footprint_path)
   r['artifact_paths'].append(footprint_path)
   r['metrics']['raw_disk_host_allocated_bytes']=footprint['raw_disk_host_allocated_bytes']
   r['metrics']['named_boot_assets_bytes']=footprint['asset_bytes_sum']
   r['footprint_observation_utc']=footprint['utc']
   r['measurement_notes']=footprint['notes']+['Guest filesystem used is the filesystem reported by kubelet; for kuberoot it excludes read-only root/ESP. Raw allocation is a separate whole-disk host measurement.']
   if r['product']=='talos-v1.14.7':
    plotted=remote('python3','/work/field-edge/scripts/chart-idle-comparison.py')
    if plotted.returncode:raise RuntimeError(plotted.stderr)
    comparison='02-edge/img/chart-idle-comparison.png'
    copy('/work/field-edge/captures/chart-idle-comparison.png',root/comparison)
    r['artifact_paths'].append(comparison)
   # Every scene is rendered and copied before the result is published.
   record=root/'02-edge/raw'/f'{stem}-published-result.json'
   record.write_text(json.dumps(r,indent=2)+'\n')
   subprocess.run(['python3',str(scripts/'update-results.py'),'--article-root',str(root),'--record',str(record)],check=True)
   with (root/'02-edge/ARTIFACTS.md').open('a') as f:
    f.write(f"\n### {r['scenario_id']} — {r['product']}\n\n")
    f.write(f"Source `{r['source_commit']}`, image `{r['image_digest']}`. {r['samples_ok']}/{r['samples_total']} samples; status {r['status']}.\n\n")
    for artifact in r['artifact_paths']:
     kind='Сравнение четырёх E-B1: working set, CPU, host raw allocation, virtual writes' if pathlib.Path(artifact).name=='chart-idle-comparison.png' else 'Измеренная временная кривая idle RAM' if pathlib.Path(artifact).name.startswith('chart-') else 'Реальный запуск и вывод E-B1' if artifact.endswith(('.gif','.png','.txt')) else 'Исходные samples E-B1'
     f.write(f'- `{artifact}` — {kind}.\n')
    f.write('\nMetrics: `'+json.dumps(r['metrics'],ensure_ascii=False)+'`.\n')
   print(json.dumps({'published':r['product'],'status':r['status']}),flush=True)
 except Exception as exc:
  print('sync error: '+str(exc),flush=True)
 time.sleep(20)
