#!/usr/bin/env python3
"""Density staircase with measured API p99 and explicit scheduler/memory limits.
Requires a pinned image that runs workload.py from the deployed field ConfigMap.
Never interpret an unready replica count as measured workload density.
"""
import argparse,json,os,pathlib,statistics,subprocess,time,datetime
p=argparse.ArgumentParser(); p.add_argument('--product',required=True); p.add_argument('--image',required=True); p.add_argument('--output',type=pathlib.Path,required=True)
a=p.parse_args(); assert '@sha256:' in a.image
a.output.mkdir(parents=True,exist_ok=False)
def k(*args, data=None):
    return subprocess.run(['kubectl','--request-timeout=10s',*args],input=data,text=True,capture_output=True,timeout=15)
def obj(*args):
    r=k(*args); assert r.returncode==0,r.stderr
    return json.loads(r.stdout)
node=obj('get','nodes','-o','json')['items'][0]['metadata']['name']
spec={'apiVersion':'apps/v1','kind':'Deployment','metadata':{'name':'density','namespace':'field'},'spec':{'replicas':0,'selector':{'matchLabels':{'app':'density'}},'template':{'metadata':{'labels':{'app':'density'}},'spec':{'containers':[{'name':'http','image':a.image,'command':['python3','/code/workload.py'],'volumeMounts':[{'name':'code','mountPath':'/code'}],'resources':{'requests':{'cpu':'1m','memory':'8Mi'},'limits':{'memory':'64Mi'}}}],'volumes':[{'name':'code','configMap':{'name':'workload'}}]}}}}
r=k('apply','-f','-',data=json.dumps(spec)); assert r.returncode==0,r.stderr
for count in [0,10,50,100,125,150,200,250]:
    r=k('-n','field','scale','deployment/density',f'--replicas={count}'); assert r.returncode==0,r.stderr
    deadline=time.monotonic()+180
    while time.monotonic()<deadline:
        d=obj('-n','field','get','deployment','density','-o','json')
        if d['status'].get('readyReplicas',0)==count: break
        time.sleep(5)
    ready=d['status'].get('readyReplicas',0)
    time.sleep(60)
    latency=[]; errors=[]
    for i in range(200):
        start=time.monotonic(); r=k('get','--raw','/readyz'); latency.append(time.monotonic()-start)
        if r.returncode or r.stdout.strip()!='ok': errors.append({'i':i,'stderr':r.stderr,'body':r.stdout})
        time.sleep(.1)
    n=obj('get','node',node,'-o','json'); summary=obj('get','--raw',f'/api/v1/nodes/{node}/proxy/stats/summary')
    pressure=any(c['type']=='MemoryPressure' and c['status']=='True' for c in n['status']['conditions'])
    p99=sorted(latency)[int(.99*(len(latency)-1))]
    limit=('memory pressure' if pressure else 'API errors' if errors else 'API p99 exceeds 1 second' if p99>1 else 'replicas did not become ready within 180 seconds' if ready<count else None)
    record={'scenario_id':'E-B3','product':a.product,'requested_pods':count,'ready_pods':ready,'api_p99_seconds':p99,'latency_definition':'outside-DUT kubectl /readyz wall time, includes process and TLS startup','latencies_seconds':latency,'errors':errors,'guest_memory':summary['node'].get('memory'),'limit':limit,'utc':datetime.datetime.now(datetime.timezone.utc).isoformat()}
    (a.output/f'{count}.json').write_text(json.dumps(record,indent=2))
    print(json.dumps({key:value for key,value in record.items() if key not in ['latencies_seconds','errors']}),flush=True)
    if limit:
        (a.output/'limit-pods.json').write_text(k('-n','field','get','pods','-o','json').stdout)
        (a.output/'limit-events.json').write_text(k('-n','field','get','events','-o','json').stdout)
        break
