#!/usr/bin/env python3
"""Serve real signed artifact bytes with deterministic download failures."""
from http.server import BaseHTTPRequestHandler,HTTPServer
from pathlib import Path
import json,time
bundle=Path('/work/www/kuberoot-0.1.1-router.field.1-amd64.tar')
class Handler(BaseHTTPRequestHandler):
 def do_GET(self):
  mode=self.path.strip('/').split('/')[0]
  if self.path.endswith('.sig'):
   p=Path(str(bundle)+'.sig');data=p.read_bytes();advertised=len(data)
  else:
   size=bundle.stat().st_size
   with bundle.open('rb') as f:data=f.read(size//4)
   advertised=size if mode=='disconnect' else len(data)
  self.send_response(200);self.send_header('Content-Length',str(advertised));self.end_headers();self.wfile.write(data);self.wfile.flush();self.close_connection=True
  print(json.dumps({'at':time.time(),'path':self.path,'served_bytes':len(data),'advertised_bytes':advertised}),flush=True)
HTTPServer(('0.0.0.0',8089),Handler).serve_forever()
