#!/usr/bin/env python3
"""Health endpoint backed by real client -> router -> peer LAN probes."""
import json,subprocess,threading,time
from http.server import BaseHTTPRequestHandler,HTTPServer
state={'ok':False,'at':0}
lock=threading.Lock()
def probe():
 while True:
  start=time.monotonic()
  try:
   p=subprocess.run(['python3','/work/client.py','c1','ping -c 1 -W 1 192.168.20.113'],capture_output=True,text=True,timeout=5)
   ok=p.returncode==0 and '1 packets transmitted, 1 received' in p.stdout
   row={'at':time.time(),'duration_s':time.monotonic()-start,'ok':ok,'output':p.stdout,'stderr':p.stderr}
  except Exception as e:row={'at':time.time(),'ok':False,'error':str(e)}
  with lock:state.update(row)
  with open('/work/field/raw/R2-forwarding-observer.jsonl','a') as f:f.write(json.dumps(row)+'\n')
  time.sleep(.5)
class Handler(BaseHTTPRequestHandler):
 def do_GET(self):
  with lock:r=dict(state)
  ok=r['ok'] and time.time()-r['at']<3
  body=json.dumps({'ok':ok,'sample_at':r['at']}).encode()
  self.send_response(200 if ok else 503);self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body)
 def log_message(self,*a):pass
threading.Thread(target=probe,daemon=True).start()
HTTPServer(('0.0.0.0',8090),Handler).serve_forever()
