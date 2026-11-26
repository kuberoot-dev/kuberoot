#!/usr/bin/env python3
"""50 real log-writing pods and one pod replacement per ten seconds.
Deploy separately before measurement; run churn until its explicit stop file.
"""
import argparse,datetime,json,os,pathlib,shlex,subprocess,time
p=argparse.ArgumentParser();p.add_argument('action',choices=['deploy','run']);p.add_argument('--output',type=pathlib.Path,required=True);p.add_argument('--stop-file',type=pathlib.Path);a=p.parse_args()
def call(args,body=None):
 print('$ '+shlex.join(args),flush=True)
 r=subprocess.run(args,input=body,text=True,capture_output=True,timeout=330)
 print(r.stdout+r.stderr,end='',flush=True)
 if r.returncode:raise RuntimeError(r.stderr)
 return r.stdout
base=['kubectl','--request-timeout=10s','-n','field']
if a.action=='deploy':
 image=os.environ['PYTHON_IMAGE'];assert '@sha256:' in image
 code='import datetime,time;\nwhile True:\n print(datetime.datetime.now(datetime.timezone.utc).isoformat()+" L"*496,flush=True);time.sleep(1)'
 obj={'apiVersion':'apps/v1','kind':'Deployment','metadata':{'name':'storage-churn','namespace':'field'},'spec':{'replicas':50,'selector':{'matchLabels':{'app':'storage-churn'}},'template':{'metadata':{'labels':{'app':'storage-churn'}},'spec':{'containers':[{'name':'log','image':image,'command':['python3','-c',code],'resources':{'requests':{'cpu':'1m','memory':'16Mi'},'limits':{'memory':'48Mi'}}}]}}}}
 a.output.write_text(json.dumps(obj,indent=2));call(base+['apply','-f',str(a.output)])
 call(['kubectl','-n','field','rollout','status','deployment/storage-churn','--timeout=300s'])
else:
 assert a.stop_file is not None
 while not a.stop_file.exists():
  row={'utc':datetime.datetime.now(datetime.timezone.utc).isoformat()}
  try:
   pods=json.loads(subprocess.check_output(base+['get','pods','-l','app=storage-churn','-o','json'],text=True,timeout=15))['items']
   ready=[x for x in pods if not x['metadata'].get('deletionTimestamp') and any(c['type']=='Ready' and c['status']=='True' for c in x.get('status',{}).get('conditions',[]))]
   row['pods']=len(pods);row['ready']=len(ready)
   if ready:
    name=min(ready,key=lambda x:x['metadata']['creationTimestamp'])['metadata']['name'];row['deleted']=name
    call(base+['delete','pod',name,'--wait=false'])
  except Exception as exc:row['error']=str(exc)
  with a.output.open('a') as f:f.write(json.dumps(row)+'\n')
  for _ in range(10):
   if a.stop_file.exists():break
   time.sleep(1)
