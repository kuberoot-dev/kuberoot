#!/usr/bin/env python3
import base64,concurrent.futures,http.client,json,os,ssl,statistics,threading,time,yaml
from pathlib import Path
cfg=yaml.safe_load(Path('/work/cluster-r1.conf').read_text());priv=Path('/work/field/private');u=cfg['users'][0]['user'];c=cfg['clusters'][0]['cluster']
for key,name in [('client-certificate-data','load-client.crt'),('client-key-data','load-client.key')]:
 p=priv/name;p.write_bytes(base64.b64decode(u[key]));p.chmod(0o600)
ctx=ssl.create_default_context(cadata=base64.b64decode(c['certificate-authority-data']).decode());ctx.load_cert_chain(str(priv/'load-client.crt'),str(priv/'load-client.key'))
tls=threading.local();start=time.monotonic();wall=time.time();rows=[]
def one(i):
 t=time.monotonic()
 try:
  if not hasattr(tls,'conn'):tls.conn=http.client.HTTPSConnection('127.0.0.1',6413,context=ctx,timeout=3)
  tls.conn.request('GET','/apis/router.kuberoot.dev/v1alpha1/interfaces');r=tls.conn.getresponse();r.read();status=r.status;error=None
 except Exception as e:
  status=None;error=str(e)
  if hasattr(tls,'conn'):tls.conn.close();del tls.conn
 return {'seq':i,'at':time.time(),'latency_s':time.monotonic()-t,'status':status,'error':error}
print(time.strftime('%FT%TZ',time.gmtime()),'authenticated GET Interface API, offered 100 requests/s for60s',flush=True)
with concurrent.futures.ThreadPoolExecutor(max_workers=32) as pool:
 futures=[]
 for i in range(6000):
  time.sleep(max(0,start+i/100-time.monotonic()));futures.append(pool.submit(one,i))
 rows=[f.result() for f in futures]
elapsed=time.monotonic()-start;lat=sorted(r['latency_s'] for r in rows);summary={'offered_rps':100,'requests':len(rows),'elapsed_s':elapsed,'completed_rps':len(rows)/elapsed,'http200':sum(r['status']==200 for r in rows),'p99_s':lat[__import__('math').ceil(.99*len(lat))-1],'max_s':max(lat),'start_wall':wall,'end_wall':time.time()}
raw=Path('/work/field/raw');(raw/'R10-kuberoot-load.json').write_text(json.dumps(rows)+'\n');(raw/'R10-kuberoot-load.summary.json').write_text(json.dumps(summary,indent=2)+'\n');print(json.dumps(summary),flush=True)
