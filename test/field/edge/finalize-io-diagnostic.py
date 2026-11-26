import hashlib,json,pathlib,shutil
root=pathlib.Path('/work/field-edge');d=root/'results/E2-diagnostic-main-pod-network-io-1';p=d/'result.json';r=json.loads(p.read_text())
assert r['measurement_protocol']=='upgrade-client-disconnect-v2'
shutil.copyfile(p,d/'driver-result-unscored.json')
r['metrics']['diagnostic_driver_status']=r['status'];r['metrics']['diagnostic_driver_reason']=r['reason']
r.update(measurement_protocol='diagnostic-host-io-v1',status='invalid-diagnostic',repetition=0,fault_phase='host-io-diagnostic-pod-network',reason='Separate metadata-only host I/O diagnostic, excluded from scored repetitions. This run did not reproduce backend errors; target B became Good with verified broken pod network. Underlying intermittent backend cause remains unknown.')
r['measurement_notes']=['QEMU-only LD_PRELOAD passes pread64/pwrite64/pwritev64 calls and results through unchanged, logging metadata only; timing is excluded from product comparison.','All recorded alignment probes selected request alignment4096 and buffer alignment512; no failed pwrite calls or QMP backend failures in this diagnostic run.']
files=[root/'scripts/qemu-io-diagnostic.c',root/'io-diagnosis/qemu-io-diagnostic.so',root/'scripts/io-diagnostic-scene.sh']
provenance={'files':[{'path':str(f),'sha256':hashlib.sha256(f.read_bytes()).hexdigest()} for f in files],'diagnostic_only':True,'cache':'none','ptrace_used':False,'privileges_changed':False}
(d/'io-diagnostic-provenance.json').write_text(json.dumps(provenance,indent=2));p.write_text(json.dumps(r,indent=2))
