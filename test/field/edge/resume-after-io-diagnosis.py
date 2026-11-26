#!/usr/bin/env python3
"""Incident-specific resume after non-scored diagnostic disk preservation."""
import json,os,pathlib,subprocess,time
root=pathlib.Path('/work/field-edge');d=root/'results/E2-diagnostic-main-pod-network-io-1'
assert 'LD_PRELOAD' not in os.environ and 'io-diagnosis/bin' not in os.environ.get('PATH','')
for _ in range(180):
 if (d/'failed.qcow2').is_file() and not (d/'system.raw').exists():break
 time.sleep(5)
else:raise SystemExit('Diagnostic preservation not complete; original queue remains stopped')
assert json.loads((d/'result.json').read_text())['status']=='invalid-diagnostic'
(d/'published').touch()
for p in pathlib.Path('/proc').glob('[0-9]*/cmdline'):
 try:a=p.read_bytes().decode().split('\0')
 except (OSError,UnicodeError):continue
 if a[0].endswith('qemu-system-x86_64'):assert '-name' in a and a[a.index('-name')+1]=='edge-field-gateway'
 if len(a)>1:assert not (pathlib.Path(a[0]).name=='bash' and a[1].endswith('run-upgrade-kuberoot.sh'))
log=(root/'e2-main-v2-backend-retries.log').open('x')
p=subprocess.Popen(['bash',str(root/'scripts/run-upgrade-kuberoot.sh'),'main'],stdout=log,stderr=subprocess.STDOUT,stdin=subprocess.DEVNULL,start_new_session=True)
(root/'e2-main-v2-backend-retries.pid').write_text(str(p.pid))
wlog=(root/'after-e2-main-v2-backend-retries.log').open('x')
w=subprocess.Popen(['python3',str(root/'scripts/continue-after-main.py'),'--parent',str(p.pid)],stdout=wlog,stderr=subprocess.STDOUT,stdin=subprocess.DEVNULL,start_new_session=True)
(root/'after-e2-main-v2-backend-retries.pid').write_text(str(w.pid));print(json.dumps({'runner':p.pid,'continuation':w.pid}),flush=True)
