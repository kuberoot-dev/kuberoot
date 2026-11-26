#!/usr/bin/env python3
"""Lightweight Mac watcher: archive completed observed hours without claiming suite pass."""
import datetime,fcntl,json,os,subprocess,time
from pathlib import Path
article=Path('/Users/tym83/projects/kuberoot-articles')
env=dict(os.environ,KUBECONFIG='/Users/tym83/workshop.txt')
cmd=['kubectl','--context','admin@workshop','-n','tenant-kuberoot','exec','kr-router-stand','--']
def get(path):
 p=subprocess.run(cmd+['cat',path],env=env,capture_output=True,timeout=30)
 return p.stdout if p.returncode==0 else None
def fetch(remote,local):
 data=get(remote)
 if data is not None:local.write_bytes(data)
 return data is not None
lock=open('/tmp/router-baseline-sync.lock','w')
try:fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
except BlockingIOError:raise SystemExit('another baseline watcher is active')
Path('/tmp/router-baseline-sync.pid').write_text(str(os.getpid()))
print('baseline watcher active',os.getpid(),flush=True)
pending={'r2','debian'}
while pending:
 for vm in sorted(pending.copy()):
  data=get('/work/field/raw/R-B1-'+vm+'-idle.summary.json')
  if not data:continue
  s=json.loads(data)
  if not s.get('observed_full_hour'):continue
  if vm=='debian' and not all(s.get(k) for k in ('valid_samples','valid_counters','stable_boot')):continue
  raw=article/'03-router/raw';prefix='R-B1-'+vm+'-idle'
  (raw/(prefix+'.summary.json')).write_bytes(data)
  if not fetch('/work/field/raw/'+prefix+'.jsonl',raw/(prefix+'.jsonl')):continue
  stamp=datetime.datetime.now(datetime.timezone.utc).isoformat()
  path=article/'results/router.jsonl';rows=[json.loads(x) for x in path.read_text().splitlines()]
  product='kuberoot' if vm=='r2' else 'debian'
  if not any(r.get('profile')=='observed-idle-hour' and r.get('product')==product for r in rows):
   record=dict.fromkeys(rows[0])
   record.update(test_id='R-B1',product=product,profile='observed-idle-hour',result='partial',recorded_at=stamp,vcpu=2,ram=2147483648,metrics=s,reason='Observed hour complete; whole R-B1 comparison remains incomplete. Shared pod performs other preparation. No throughput or whole-suite pass inferred.',artifact_paths=['03-router/raw/'+prefix+'.jsonl','03-router/raw/'+prefix+'.summary.json'])
   if vm=='r2':record.update(version='0.1.1-router.capture.1',commit='ffcc71a4c6c7d3aa36c37221f0cd1d19c22e99b0',image_sha256='0f395beb4dc316e80e6409b751ede0c6fc5bf844d641730a10fcf697616d6b13',topology='phase1 socket LAN/transit, user-net WAN; idle observation only')
   else:record.update(image_sha256='b94ebc662796cbd4fd1f5053ce63b59ebc56e034f8f550689713c0cae493310e',topology='socket client -> Debian DUT -> Linux gateway -> sink')
   with path.open('a') as f:f.write(json.dumps(record,ensure_ascii=False)+'\n')
   summary=f'\n## Автоматически сохранённый час {product} — {stamp}\n\nНаблюдалось {s["elapsed_s"]:.2f}s, {s["samples"]} samples. Запись диска: {s.get("disk_write_bytes")}B. Полный raw: `raw/{prefix}.jsonl`; summary: `raw/{prefix}.summary.json`. Это завершённое окно idle, а не прохождение всей R-B1/таблицы.\n'
   with (article/'03-router/RESULTS.md').open('a') as f:f.write(summary)
   with (article/'03-router/STAND_LOG.md').open('a') as f:f.write(summary)
   with (article/'03-router/ARTIFACTS.md').open('a') as f:f.write(f'\n- [Observed idle {product}](raw/{prefix}.jsonl), [summary](raw/{prefix}.summary.json) — R-B1, реальный полный час; не означает завершение сравнения.\n')
  pending.remove(vm);print(stamp,'archived',vm,flush=True)
 if pending:time.sleep(40)
