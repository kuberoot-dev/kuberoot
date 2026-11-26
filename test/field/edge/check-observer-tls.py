#!/usr/bin/env python3
"""Local TLS fixture only: not a Kubernetes or field product result."""
import base64,http.server,json,os,pathlib,ssl,subprocess,tempfile,threading
assert os.uname().sysname=='Linux' and pathlib.Path('/work/field-edge').is_dir()
s=pathlib.Path(__file__).resolve().parent
class API(http.server.BaseHTTPRequestHandler):
 objects={}
 def log_message(self,*args):pass
 def answer(self,body):
  self.send_response(200);self.end_headers();self.wfile.write(json.dumps(body).encode())
 def do_POST(self):
  body=json.loads(self.rfile.read(int(self.headers['Content-Length'])))
  self.objects[body['metadata']['name']]=body;self.answer(body)
 def do_GET(self):self.answer(self.objects[self.path.rsplit('/',1)[1]])
 def do_DELETE(self):self.answer({})
class W(http.server.BaseHTTPRequestHandler):
 def log_message(self,*args):pass
 def do_GET(self):self.send_response(200);self.end_headers();self.wfile.write(b'W'*1024)
 def do_POST(self):
  body=self.rfile.read(int(self.headers['Content-Length']));self.send_response(200);self.end_headers();self.wfile.write(body[:20])
with tempfile.TemporaryDirectory(dir='/work/tmp') as name:
 d=pathlib.Path(name)
 subprocess.run(['openssl','req','-x509','-newkey','ec','-pkeyopt','ec_paramgen_curve:prime256v1','-nodes','-subj','/CN=observer.fixture','-addext','subjectAltName=DNS:observer.fixture','-days','1','-keyout',str(d/'key'),'-out',str(d/'cert')],check=True,capture_output=True)
 context=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);context.load_cert_chain(d/'cert',d/'key');context.load_verify_locations(d/'cert');context.verify_mode=ssl.CERT_REQUIRED
 api=http.server.ThreadingHTTPServer(('127.0.0.1',0),API);api.socket=context.wrap_socket(api.socket,server_side=True)
 workload=http.server.ThreadingHTTPServer(('127.0.0.1',0),W)
 for server in [api,workload]:threading.Thread(target=server.serve_forever,daemon=True).start()
 encode=lambda p:base64.b64encode(p.read_bytes()).decode()
 for index,tls_name in enumerate(['observer.fixture','wrong.fixture',None]):
  cluster={'server':f'https://127.0.0.1:{api.server_port}','certificate-authority-data':encode(d/'cert')}
  if tls_name:cluster['tls-server-name']=tls_name
  config={'current-context':'fixture','contexts':[{'name':'fixture','context':{'cluster':'fixture','user':'fixture'}}],'clusters':[{'name':'fixture','cluster':cluster}],'users':[{'name':'fixture','user':{'client-certificate-data':encode(d/'cert'),'client-key-data':encode(d/'key')}}]}
  path=d/'config';path.write_text(json.dumps(config));path.chmod(0o600)
  output=d/f'{index}.jsonl';url=f'http://127.0.0.1:{workload.server_port}'
  subprocess.run(['python3',str(s/'observer.py'),'--http',url,'--append',url,'--kubeconfig',str(path),'--seconds','.1','--output',str(output)],check=True)
  results=[json.loads(line) for line in output.read_text().splitlines()];probes=[r for r in results if r['kind']=='api'];assert len(probes)==1
  assert probes[0]['ok']==(index==0),probes
  if index:assert 'CERTIFICATE_VERIFY_FAILED' in probes[0]['error'],probes
  assert all(r['ok'] for r in results if r['kind']!='api')
 for server in [api,workload]:server.shutdown();server.server_close()
print('PASS: valid hostfwd TLS name accepted; wrong/missing names rejected; client certificate required. Synthetic harness only.')
