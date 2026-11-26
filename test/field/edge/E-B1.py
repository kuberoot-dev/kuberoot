#!/usr/bin/env python3
"""Thirty-minute idle measurement. Run only after installed boot settles.

Guest telemetry comes from kubelet, block writes from QMP. Never use QEMU RSS
as guest RAM. Keep every sample, including failed requests. No pass gate.
"""
import argparse, datetime, json, os, pathlib, statistics, subprocess, time
from qmp import command

ap = argparse.ArgumentParser()
ap.add_argument('--product', required=True)
ap.add_argument('--image-digest', required=True)
ap.add_argument('--source-commit', required=True)
ap.add_argument('--qmp', required=True)
ap.add_argument('--output', type=pathlib.Path, required=True)
a = ap.parse_args()
a.output.mkdir(parents=True, exist_ok=False)

def kubectl(*args):
    return subprocess.check_output(['kubectl', '--request-timeout=15s', *args], timeout=20, text=True)

nodes = json.loads(kubectl('get','nodes','-o','json'))['items']
assert len(nodes) == 1, 'baseline requires one node'
node = nodes[0]['metadata']['name']
(a.output/'inventory.json').write_text(kubectl('get','pods','-A','-o','json'))
(a.output/'node.json').write_text(json.dumps(nodes[0],indent=2))
print('$ date -u', flush=True)
print(subprocess.check_output(['date','-u'],text=True),end='',flush=True)
print('$ kubectl get nodes', flush=True)
print(kubectl('get','nodes'), flush=True)
print('Sampling kubelet whole-node stats and QMP block counters for 1800 seconds', flush=True)
start = time.monotonic()
samples = []
with (a.output/'samples.jsonl').open('x') as f:
    for i in range(61):
        time.sleep(max(0,start+i*30-time.monotonic()))
        r={'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(), 'elapsed_seconds':time.monotonic()-start}
        try:
            r['guest']=json.loads(kubectl('get','--raw',f'/api/v1/nodes/{node}/proxy/stats/summary'))['node']
            r['block']=command(a.qmp,'query-blockstats')
        except Exception as exc:
            r['error']=str(exc)
        f.write(json.dumps(r)+'\n'); f.flush(); os.fsync(f.fileno())
        samples.append(r)
        if i % 10 == 0:
            print(json.dumps({'utc':r['utc'],'sample':i,'memory':r.get('guest',{}).get('memory'),'error':r.get('error')}),flush=True)
good=[r for r in samples if 'error' not in r]
metrics={}
if len(good)>1:
    first,last=good[0],good[-1]
    duration=last['elapsed_seconds']-first['elapsed_seconds']
    metrics['observed_seconds']=duration
    for key in ['usageBytes','workingSetBytes','availableBytes']:
        vals=[r['guest']['memory'][key] for r in good if key in r['guest'].get('memory',{})]
        metrics['guest_memory_'+key+'_median']=statistics.median(vals) if vals else None
    metrics['cpu_cores']=(last['guest']['cpu']['usageCoreNanoSeconds']-first['guest']['cpu']['usageCoreNanoSeconds'])/1e9/duration
    metrics['guest_filesystem_used_bytes']=last['guest'].get('fs',{}).get('usedBytes')
    def writes(r):
        return sum(b['stats']['wr_bytes'] for b in r['block'])
    metrics['virtual_block_write_bytes_per_hour']=(writes(last)-writes(first))*3600/duration
result={'scenario_id':'E-B1','product':a.product,'image_digest':a.image_digest,'source_commit':a.source_commit,
 'topology':'single DUT; socket multicast LAN; Debian gateway NAT; SSH tunnel only for authenticated observation',
 'vcpu':2,'ram_mib':4096,'disk':'32 GiB sparse raw, cache=none, virtio, UEFI',
 'fault_seed':None,'fault_phase':'settled idle','outage_seconds':None,'acknowledged_record_loss':None,
 'recovery_seconds':None,'manual_actions':[], 'status':'measured' if len(good)==61 else 'incomplete',
 'reason':'no pass gate; all raw samples retained', 'metrics':metrics,'samples_ok':len(good),'samples_total':61,
 'time_utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'artifact_paths':[str(a.output)]}
(a.output/'result.json').write_text(json.dumps(result,indent=2)+'\n')
print(json.dumps(result,indent=2),flush=True)
