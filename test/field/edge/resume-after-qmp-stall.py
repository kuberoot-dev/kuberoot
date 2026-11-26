#!/usr/bin/env python3
"""Incident-specific resume after the QMP single-client stall of init-crash-3-attempt2.

The trial passed and was published, but its driver could not deliver `quit`
because an external passive recorder held qmp.sock. This helper only waits for
that DUT to exit (it does not stop it), then performs exactly the steps the
runner would have taken: preserve the stopped disk, mark the case published and
restart the main lane plus its continuation watcher.
"""
import json,os,pathlib,subprocess,time
root=pathlib.Path('/work/field-edge');scripts=root/'scripts'
d=root/'results/E2-v2-kuberoot-main-1b29543-init-crash-3-attempt2'
pid=int((d/'pid').read_text())
def alive():
 try:return pathlib.Path(f'/proc/{pid}/stat').read_text().rsplit(')',1)[1].split()[0]!='Z'
 except FileNotFoundError:return False
print('waiting for stopped DUT',pid,flush=True)
while alive():time.sleep(10)
r=json.loads((d/'result.json').read_text());assert r['status']=='passed',r['status']
rows=[json.loads(x) for x in (root/'results.jsonl').read_text().splitlines()]
assert any(x.get('time_utc')==r['time_utc'] for x in rows),'result not published'
for p in pathlib.Path('/proc').glob('[0-9]*/cmdline'):
 try:a=p.read_bytes().decode().split('\0')
 except (OSError,UnicodeError):continue
 if a[0].endswith('qemu-system-x86_64'):assert '-name' in a and a[a.index('-name')+1]=='edge-field-gateway',a
 if len(a)>1:assert not (pathlib.Path(a[0]).name=='bash' and a[1].endswith('run-upgrade-kuberoot.sh'))
first=root/'results/E2-v2-kuberoot-main-1b29543-init-crash-1/failed.qcow2'
subprocess.run(['bash','-c','source "$1/lib.sh"; source "$1/select-product.sh" main; bash "$1/preserve-failed-disk.sh" "$2" "$VM_DIR" "$3"','_',str(scripts),str(d/'system.raw'),str(first)],check=True)
(d/'published').touch()
log=(root/'e2-main-v2-after-qmp-stall.log').open('x')
p=subprocess.Popen(['bash',str(scripts/'run-upgrade-kuberoot.sh'),'main'],stdout=log,stderr=subprocess.STDOUT,stdin=subprocess.DEVNULL,start_new_session=True)
(root/'e2-main-v2-after-qmp-stall.pid').write_text(str(p.pid))
wlog=(root/'after-e2-main-v2-after-qmp-stall.log').open('x')
w=subprocess.Popen(['python3',str(scripts/'continue-after-main.py'),'--parent',str(p.pid)],stdout=wlog,stderr=subprocess.STDOUT,stdin=subprocess.DEVNULL,start_new_session=True)
(root/'after-e2-main-v2-after-qmp-stall.pid').write_text(str(w.pid))
print(json.dumps({'runner':p.pid,'continuation':w.pid}),flush=True)
