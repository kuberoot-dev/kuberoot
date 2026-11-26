#!/usr/bin/env python3
import datetime,json,os,pathlib
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
root=pathlib.Path('/work/field-edge');product=os.environ['PRODUCT'];d=root/'results'/('E-B3-'+product)
points=sorted([json.loads(p.read_text()) for p in d.glob('[0-9]*.json')],key=lambda r:r['requested_pods'])
assert points
plt.rcParams.update({'font.size':18})
fig,axes=plt.subplots(2,1,figsize=(9,10),sharex=True)
x=[r['ready_pods'] for r in points]
axes[0].plot(x,[r['guest_memory']['workingSetBytes']/2**20 for r in points],'o-',label='Working set')
axes[0].plot(x,[r['guest_memory']['usageBytes']/2**20 for r in points],'o-',label='Usage incl. cache')
axes[0].set_ylabel('Guest RAM (MiB)');axes[0].legend()
axes[1].plot(x,[r['api_p99_seconds']*1000 for r in points],'o-');axes[1].axhline(1000,color='red',linestyle='--',label='1 s threshold');axes[1].set_yscale('log');axes[1].set_ylabel('API p99 (ms, log scale)');axes[1].set_xlabel('Ready test pods');axes[1].legend()
for ax in axes:ax.grid(alpha=.3)
fig.suptitle(product+'\nDensity, 2 vCPU / 4 GiB');fig.tight_layout();chart='chart-density-'+product+'.png';fig.savefig(root/'captures'/chart,dpi=150)
name='t-E-B3-'+product;transfers={}
for ext in ['gif','png','txt','cast','timing.json']:
 transfers['02-edge/'+('img/' if ext in ['gif','png'] else 'raw/')+name+'.'+ext]=str(root/'captures'/(name+'.'+ext))
transfers['02-edge/img/'+chart]=str(root/'captures'/chart)
for p in d.glob('*.json'):transfers['02-edge/raw/E-B3-'+product+'-'+p.name]=str(p)
r={'scenario_id':'E-B3','product':product,'source_commit':os.environ['SOURCE_COMMIT'],'image_digest':os.environ['IMAGE_DIGEST'],'time_utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'vcpu':2,'ram_mib':4096,'topology':'single DUT, multicast LAN, Debian NAT gateway','fault_seed':None,'fault_phase':'density staircase','outage_seconds':None,'acknowledged_record_loss':None,'recovery_seconds':None,'manual_actions':[],'status':'measured','reason':points[-1]['limit'] or 'largest configured step reached; no inferred capacity beyond it','metrics':{'largest_ready_test_pods':max(x),'requested_at_last_step':points[-1]['requested_pods'],'last_api_p99_seconds':points[-1]['api_p99_seconds']},'measurement_notes':['Default system pods are additional to the stated test-pod counts. Fresh strict-mTLS HTTPS connection per API request. Scheduler pod-count ceiling is reported as a scheduling limit, not memory capacity.'],'artifact_paths':list(transfers),'artifact_transfers':transfers}
(d/'result.json').write_text(json.dumps(r,indent=2))
with (root/'results.jsonl').open('a') as f:f.write(json.dumps(r)+'\n')
