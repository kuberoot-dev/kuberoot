#!/usr/bin/env python3
"""Archive evidence and append a scoped observation without claiming a whole row."""
import argparse,datetime,json,os,subprocess
from pathlib import Path
p=argparse.ArgumentParser();p.add_argument('test');p.add_argument('product');p.add_argument('profile');p.add_argument('note');p.add_argument('files',nargs='+');a=p.parse_args()
root=Path('/Users/tym83/projects/kuberoot-articles');env=dict(os.environ,KUBECONFIG='/Users/tym83/workshop.txt')
paths=[]
for name in a.files:
 remote='/work/field/'+name;local=root/'03-router'/name;local.parent.mkdir(exist_ok=True,parents=True)
 proc=subprocess.run(['kubectl','--context','admin@workshop','-n','tenant-kuberoot','exec','kr-router-stand','--','cat',remote],env=env,capture_output=True,check=True)
 local.write_bytes(proc.stdout);paths.append(str(local.relative_to(root)))
stamp=datetime.datetime.now(datetime.timezone.utc).isoformat()
row={'test_id':a.test,'product':a.product,'profile':a.profile,'result':'partial','recorded_at':stamp,'reason':a.note,'artifact_paths':paths}
with (root/'results/router.jsonl').open('a') as f:f.write(json.dumps(row,ensure_ascii=False)+'\n')
for doc in ['RESULTS.md','STAND_LOG.md']:
 with (root/'03-router'/doc).open('a') as f:f.write(f'\n## {a.test} / {a.product} / {a.profile} — {stamp}\n\n{a.note} Полная строка пока не завершена. Доказательства: '+', '.join(f'`{x}`' for x in paths)+'\n')
with (root/'03-router/ARTIFACTS.md').open('a') as f:
 for name in a.files:f.write(f'\n- [{Path(name).name}]({name}) — {a.test}, {a.product}, {a.profile}; {a.note}\n')
