#!/usr/bin/env python3
"""Exact Mac-side maintenance command for the reviewed E2 storage retry.
Incident-specific PIDs/paths are intentional; do not rerun after completion.
"""
import datetime,json,os,pathlib,shutil,subprocess,time
root=pathlib.Path('/Users/tym83/projects/kuberoot-articles');scripts=pathlib.Path('/Users/tym83/projects/kuberoot-field-edge/test/field/edge');archive=pathlib.Path('/Users/tym83/projects/kuberoot-field-edge-archive')
env=dict(os.environ,KUBECONFIG='/Users/tym83/workshop.txt');base=['kubectl','--context','admin@workshop','-n','tenant-kuberoot','exec','kr-edge-stand','--']
def remote(*args):return subprocess.check_output(base+list(args),env=env,text=True)
while True:
 ready=remote('python3','-c',"import pathlib;d=pathlib.Path('/work/field-edge/results/E2-v2-kuberoot-main-1b29543-init-crash-2-attempt2');c=pathlib.Path('/work/field-edge/captures/t-E2-v2-kuberoot-main-1b29543-init-crash-2-attempt2.png');print(int((d/'result.json').exists() and c.exists()))").strip()
 if ready=='1':break
 time.sleep(15)
for source,name in [('/work/fleet/n2/sys.qcow2','phase1-fleet-n2-sys'),('/work/fleet/n3/sys.qcow2','phase1-fleet-n3-sys'),('/work/upgrade-demo/n2/sys.qcow2','phase1-upgrade-n2-sys')]:
 size=int(remote('stat','-c','%s',source));assert shutil.disk_usage(archive).free>size+2*1024**3,'Mac free-space reserve would be exhausted'
 subprocess.run(['python3',str(scripts/'archive-stopped-disk.py'),source,str(archive/(name+'.qcow2'))],check=True)
 manifest=json.loads((archive/(name+'.json')).read_text());(root/'02-edge/raw'/(name+'-archive.json')).write_text(json.dumps(manifest,indent=2)+'\n')
 with (root/'02-edge/STAND_LOG.md').open('a') as f:f.write('\n## '+datetime.datetime.now(datetime.timezone.utc).isoformat()+' — forensic archive\n\n- '+source+' → '+manifest['archive']+'; bytes='+str(manifest['bytes'])+', SHA256='+manifest['sha256']+'. Stream hash и повторный source hash совпали; только после этого source в собственном PVC удалён. Restore path сохранён в raw/'+name+'-archive.json.\n')
 with (root/'02-edge/ARTIFACTS.md').open('a') as f:f.write('\n- `raw/'+name+'-archive.json` — местоположение и проверка SHA256 сохранённого Phase-1 диска; это capacity maintenance, не продуктовый результат.\n')
 print('archived '+name,flush=True)
print(remote('df','-h','/work'),flush=True)
print(remote('python3','-c','import os,pathlib,signal;p=pathlib.Path("/proc/111316/cmdline");assert b"/work/field-edge/scripts/run-upgrade-kuberoot.sh" in p.read_bytes().split(b"\\0");os.kill(111316,signal.SIGCONT);print("E2 runner resumed")'),flush=True)
