import json,pathlib,subprocess,os
root=pathlib.Path('/work/field-edge');d=root/'results/E2-v2-kuberoot-main-1b29543-init-crash-2'
assert (d/'failed.qcow2').is_file() and not (d/'system.raw').exists()
assert json.loads((d/'result.json').read_text())['status']=='invalid-fixture'
assert any(json.loads(l).get('time_utc')==json.loads((d/'result.json').read_text())['time_utc'] for l in (root/'results.jsonl').read_text().splitlines())
for p in pathlib.Path('/proc').glob('[0-9]*/cmdline'):
 try:a=p.read_bytes().decode().split('\0')
 except (OSError,UnicodeError):continue
 if a[0].endswith('qemu-system-x86_64'):assert '-name' in a and a[a.index('-name')+1]=='edge-field-gateway'
 if len(a)>1:assert not (pathlib.Path(a[0]).name=='bash' and a[1].endswith('run-upgrade-kuberoot.sh'))
(d/'published').touch()
(d/'retry.json').write_text(json.dumps({'directory':d.name+'-attempt2','reason':'Unexpected preboot QEMU write errors; intended init-crash never reached. Original invalid result and disk preserved with full comparison. Eight scratch direct-I/O write/flush/read regions passed; cause unknown. Repeat same raw/cache=none VM and fabric once.'},indent=2))
log=(root/'e2-main-v2-reviewed-retry.log').open('x')
p=subprocess.Popen(['bash',str(root/'scripts/run-upgrade-kuberoot.sh'),'main'],stdout=log,stderr=subprocess.STDOUT,stdin=subprocess.DEVNULL,start_new_session=True)
(root/'e2-main-v2-reviewed-retry.pid').write_text(str(p.pid))
wlog=(root/'after-e2-main-v2-reviewed-retry.log').open('x')
w=subprocess.Popen(['python3',str(root/'scripts/continue-after-main.py'),'--parent',str(p.pid)],stdout=wlog,stderr=subprocess.STDOUT,stdin=subprocess.DEVNULL,start_new_session=True)
(root/'after-e2-main-v2-reviewed-retry.pid').write_text(str(w.pid));print(json.dumps({'runner':p.pid,'continuation':w.pid}))
