import json,pathlib,shutil
root=pathlib.Path('/work/field-edge');d=root/'results/E2-v2-kuberoot-main-1b29543-init-crash-2'
p=d/'result.json';r=json.loads(p.read_text());assert r['status']=='invalid-fixture'
shutil.copyfile(p,d/'result-before-storage-diagnosis.json')
c=json.loads((d/'block-counters.json').read_text());errors=sum(b.get('stats',{}).get(k,0) for b in c for k in ['failed_wr_operations','failed_rd_operations','failed_flush_operations'])
r['metrics']['unexpected_block_io_failures']=errors;r['metrics']['preboot_failure_message']=json.loads((d/'preboot-failure.json').read_text())['status']['message']
r['reason']+='; QEMU reported '+str(errors)+' unexpected backend I/O errors before target boot; native failure: '+r['metrics']['preboot_failure_message']+'; backend cause unknown'
r['manual_actions'].append('Stopped outer runner after preboot EIO to prevent next trial; measurement driver and client observer finished normally without recovery commands')
for source,target in [('t-E2-main-init-crash-2-unexpected-eio','storage-diagnosis'),('t-E2-main-init-crash-2-storage-probe','storage-probe')]:
 for ext in ['gif','png','txt','cast']:shutil.copyfile(root/'captures'/f'{source}.{ext}',d/f'{target}.{ext}')
probe=(d/'storage-probe.txt').read_text();assert probe.count('wrote 1048576/1048576')==8 and probe.count('read 1048576/1048576')==8
r['metrics']['scratch_probe']={'verified_write_flush_read_regions':8,'bytes_per_region':1048576,'cache':'none','scope':'separate disposable PVC file; success does not establish original EIO cause or fix'}
p.write_text(json.dumps(r,indent=2))
