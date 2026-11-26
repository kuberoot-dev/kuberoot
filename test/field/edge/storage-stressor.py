#!/usr/bin/env python3
"""Guest-side ENOSPC injector, served by a preloaded Kubernetes workload.

Mount a dedicated directory on the guest state filesystem at /state. fallocate
allocates guest filesystem extents, while avoiding 30 GiB of zero writes to the
shared lab PVC. This models filesystem fullness, not flash wear. /relieve works
without the Kubernetes API. Never mount the workshop host/PVC into this pod.
"""
import errno,http.server,json,os,pathlib,threading
cancel_fill=threading.Event()
root=pathlib.Path('/state'); target=root/'field-enospc'; root.mkdir(exist_ok=True)
class Handler(http.server.BaseHTTPRequestHandler):
 def do_POST(self):
  error=None; inventory=None
  try:
   if self.path=='/inventory':
    inventory=[]
    for prefix in ['kine','etcd/member','kuberoot/backups','rancher/k3s/server/db']:
     directory=pathlib.Path('/guest-state')/prefix
     for f in directory.rglob('*') if directory.exists() else []:
      if f.is_file() and not f.is_symlink():
       st=f.stat();inventory.append({'path':str(f.relative_to('/guest-state')),'bytes':st.st_size,'allocated_bytes':st.st_blocks*512,'mtime':st.st_mtime})
   elif self.path=='/relieve':
    cancel_fill.set();target.unlink(missing_ok=True)
   elif self.path.startswith('/fill/'):
    pct=int(self.path.rsplit('/',1)[1]); assert pct in (90,95,100)
    cancel_fill.clear()
    target.unlink(missing_ok=True)
    st=os.statvfs(root); total=st.f_blocks*st.f_frsize; used=(st.f_blocks-st.f_bfree)*st.f_frsize
    amount=max(0,int(total*pct/100)-used)
    with target.open('wb') as f:
     allocated=0; chunk=64*1024**2
     while allocated<amount and not cancel_fill.is_set():
      size=min(chunk,amount-allocated)
      try: os.posix_fallocate(f.fileno(),allocated,size); allocated+=size
      except OSError as exc:
       if exc.errno!=errno.ENOSPC: raise
       if chunk<=4096: error='ENOSPC reached';break
       chunk=max(4096,chunk//16)
     os.fsync(f.fileno())
   else: raise ValueError('unknown operation')
  except Exception as exc: error=str(exc)
  st=os.statvfs(root); data=json.dumps({'error':error,'datastore_and_backup_files':inventory,'free_bytes':st.f_bfree*st.f_frsize,'available_bytes':st.f_bavail*st.f_frsize,'total_bytes':st.f_blocks*st.f_frsize}).encode()
  self.send_response(200 if error is None else 507);self.send_header('Content-Length',str(len(data)));self.end_headers();self.wfile.write(data)
http.server.ThreadingHTTPServer(('0.0.0.0',8081),Handler).serve_forever()
