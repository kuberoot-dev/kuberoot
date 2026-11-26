#!/usr/bin/env python3
"""Read only existing workload pods' network counters; no debug pod or changes."""
import datetime,json,os,pathlib,subprocess,sys
out=pathlib.Path(sys.argv[1]);out.parent.mkdir(parents=True,exist_ok=True)
base=['kubectl','--request-timeout=8s','-n','field']
pods=json.loads(subprocess.check_output(base+['get','pods','-o','json']))
rows=[]
for pod in pods['items']:
 if pod['status'].get('phase')!='Running':continue
 name=pod['metadata']['name']
 r=subprocess.run(base+['exec',name,'--','cat','/proc/net/netstat','/proc/net/snmp'],text=True,capture_output=True,timeout=15)
 counters={};lines=r.stdout.splitlines()
 for i in range(0,len(lines)-1,2):
  keys=lines[i].split();vals=lines[i+1].split()
  if keys[0]==vals[0]:counters[keys[0].rstrip(':')]=dict(zip(keys[1:],map(int,vals[1:])))
 rows.append({'pod':name,'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'returncode':r.returncode,'stderr':r.stderr,'counters':counters})
 print(json.dumps({'pod':name,'tcp_errors':{k:v for k,v in counters.get('TcpExt',{}).items() if v and any(x in k for x in ['Drop','Overflow','Timeout','Retrans','Abort'])}}),flush=True)
out.write_text(json.dumps(rows,indent=2))
