#!/usr/bin/env python3
"""Read-only local test registry: map repository/tag names to verified OCI layouts.
Optional corrupt-blob case sends altered bytes under the original digest, to
exercise real downloader integrity checks. All requests log bytes and Range.
"""
import argparse,http.server,json,pathlib,ssl,threading,time,urllib.parse
p=argparse.ArgumentParser();p.add_argument('--catalog',type=pathlib.Path,required=True);p.add_argument('--port',type=int,default=15005);p.add_argument('--cert');p.add_argument('--key');p.add_argument('--log',type=pathlib.Path,required=True);a=p.parse_args()
catalog=json.loads(a.catalog.read_text());lock=threading.Lock()
class Handler(http.server.BaseHTTPRequestHandler):
 def do_HEAD(self):self.serve(False)
 def do_GET(self):self.serve(True)
 def serve(self,body):
  start=time.time();path=urllib.parse.urlsplit(self.path).path;sent=0
  try:
   if path in ['/v2','/v2/']:
    self.send_response(200);self.send_header('Docker-Distribution-API-Version','registry/2.0');self.send_header('Content-Length','2');self.end_headers()
    if body:self.wfile.write(b'{}');sent=2
    return
   parts=path.removeprefix('/v2/').rsplit('/',2);repo,kind,reference=parts
   item=catalog[repo];root=pathlib.Path(item['layout']);index=json.loads((root/'index.json').read_text());desc=index['manifests'][0]
   manifest=json.loads((root/'blobs/sha256'/desc['digest'][7:]).read_text())
   if kind=='manifests':
    if reference not in [item['tag'],desc['digest']]:raise KeyError(reference)
    descriptor=desc
   elif kind=='blobs':descriptor=next(x for x in [manifest['config'],*manifest['layers']] if x['digest']==reference)
   else:raise KeyError(kind)
   algorithm,digest=descriptor['digest'].split(':');assert algorithm=='sha256' and len(digest)==64
   file=root/'blobs/sha256'/digest;size=file.stat().st_size
   lower,upper=0,size-1;status=200
   if self.headers.get('Range'):
    prefix,values=self.headers['Range'].split('=',1);assert prefix=='bytes' and ',' not in values
    low,high=values.split('-',1);lower=int(low);upper=min(size-1,int(high)) if high else size-1;assert 0<=lower<=upper<size;status=206
   self.send_response(status);self.send_header('Content-Type',descriptor['mediaType']);self.send_header('Docker-Content-Digest',descriptor['digest']);self.send_header('Content-Length',str(upper-lower+1));self.send_header('Accept-Ranges','bytes')
   if status==206:self.send_header('Content-Range',f'bytes {lower}-{upper}/{size}')
   self.end_headers()
   if body:
    with file.open('rb') as f:
     f.seek(lower)
     while sent<upper-lower+1:
      chunk=f.read(min(65536,upper-lower+1-sent))
      if not chunk:break
      if kind=='blobs' and item.get('corrupt_blob')==reference and lower+sent==0:chunk=bytes([chunk[0]^1])+chunk[1:]
      self.wfile.write(chunk);sent+=len(chunk)
  except (KeyError,ValueError,AssertionError,StopIteration):self.send_error(404)
  except (BrokenPipeError,ConnectionResetError):pass
  finally:
   with lock,a.log.open('a') as f:f.write(json.dumps({'utc_epoch':start,'duration_seconds':time.time()-start,'method':self.command,'path':path,'range':self.headers.get('Range'),'bytes_sent':sent})+'\n')
server=http.server.ThreadingHTTPServer(('0.0.0.0',a.port),Handler)
if a.cert:
 context=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);context.load_cert_chain(a.cert,a.key);server.socket=context.wrap_socket(server.socket,server_side=True)
server.serve_forever()
