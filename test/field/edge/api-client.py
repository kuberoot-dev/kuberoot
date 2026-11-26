#!/usr/bin/env python3
"""Strict mTLS client built from the selected kubectl context. Never logs keys."""
import base64,http.client,json,pathlib,socket,ssl,subprocess,tempfile,urllib.parse
class Client:
 def __init__(self):
  c=json.loads(subprocess.check_output(['kubectl','config','view','--raw','--minify','-o','json']))
  cluster=c['clusters'][0]['cluster'];user=c['users'][0]['user']
  assert cluster['server'].startswith('https://') and not cluster.get('insecure-skip-tls-verify')
  self.directory=tempfile.TemporaryDirectory(prefix='field-api-')
  cert=pathlib.Path(self.directory.name)/'cert';key=pathlib.Path(self.directory.name)/'key'
  for path,field in [(cert,'client-certificate-data'),(key,'client-key-data')]:
   path.write_bytes(base64.b64decode(user[field]));path.chmod(0o600)
  self.tls=ssl.create_default_context(cadata=base64.b64decode(cluster['certificate-authority-data']).decode());self.tls.load_cert_chain(cert,key)
  self.server=urllib.parse.urlsplit(cluster['server'])
  self.tls_name=cluster.get('tls-server-name',self.server.hostname)
 def get(self,path):
  conn=http.client.HTTPSConnection(self.server.hostname,self.server.port or 443,timeout=2,context=self.tls)
  raw=socket.create_connection((self.server.hostname,self.server.port or 443),timeout=2)
  try:
   conn.sock=self.tls.wrap_socket(raw,server_hostname=self.tls_name)
   conn.request('GET',path);response=conn.getresponse();body=response.read()
   if response.status!=200:raise RuntimeError(f'HTTP {response.status}: {body[:200]!r}')
   return body
  finally:conn.close();raw.close()
 def close(self):self.directory.cleanup()
