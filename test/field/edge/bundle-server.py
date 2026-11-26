#!/usr/bin/env python3
"""Serve only named public test bundles, with actual byte/Range request evidence."""
import argparse,datetime,http.server,json,pathlib,threading,time,urllib.parse
p=argparse.ArgumentParser();p.add_argument('--directory',type=pathlib.Path,required=True);p.add_argument('--log',type=pathlib.Path,required=True);p.add_argument('--port',type=int,default=15004);a=p.parse_args()
root=a.directory.resolve();assert root==pathlib.Path('/work/field-edge/www');lock=threading.Lock()
class Handler(http.server.BaseHTTPRequestHandler):
 def do_GET(self):self.serve(True)
 def do_HEAD(self):self.serve(False)
 def serve(self,body):
  started=time.time();sent=0;status=500;error=None
  try:
   name=urllib.parse.unquote(urllib.parse.urlsplit(self.path).path).lstrip('/')
   if '/' in name or not name.endswith(('.tar','.tar.sig','.age')):raise FileNotFoundError(name)
   path=(root/name).resolve()
   if path.parent!=root or not path.is_file():raise FileNotFoundError(name)
   size=path.stat().st_size;lower=0;upper=size-1;status=200
   if self.headers.get('Range'):
    unit,span=self.headers['Range'].split('=',1);low,high=span.split('-',1)
    assert unit=='bytes' and ',' not in span and low
    lower=int(low);upper=min(size-1,int(high)) if high else size-1
    assert 0<=lower<=upper<size
    status=206
   self.send_response(status);self.send_header('Content-Length',str(upper-lower+1));self.send_header('Accept-Ranges','bytes');self.send_header('Content-Type','application/octet-stream')
   if status==206:self.send_header('Content-Range',f'bytes {lower}-{upper}/{size}')
   self.end_headers()
   if body:
    with path.open('rb') as f:
     f.seek(lower)
     while sent<upper-lower+1:
      data=f.read(min(65536,upper-lower+1-sent))
      if not data:break
      self.wfile.write(data);sent+=len(data)
  except FileNotFoundError:status=404;self.send_error(status)
  except (ValueError,AssertionError):status=416;self.send_error(status)
  except (BrokenPipeError,ConnectionResetError) as exc:error=type(exc).__name__
  finally:
   row={'utc':datetime.datetime.fromtimestamp(started,datetime.timezone.utc).isoformat(),'duration_seconds':time.time()-started,'path':self.path,'method':self.command,'range':self.headers.get('Range'),'bytes_sent':sent,'status':status,'error':error}
   with lock,a.log.open('a') as f:f.write(json.dumps(row)+'\n')
http.server.ThreadingHTTPServer(('0.0.0.0',a.port),Handler).serve_forever()
