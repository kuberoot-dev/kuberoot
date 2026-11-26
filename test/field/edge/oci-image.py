#!/usr/bin/env python3
"""Download verified OCI blobs or package a local test extension, without Docker.
Run in the workshop pod. Credentials/tokens are never printed. Pins are saved.
"""
import argparse,hashlib,json,pathlib,re,tarfile,urllib.request,urllib.error,urllib.parse,os
p=argparse.ArgumentParser();sub=p.add_subparsers(dest='command',required=True)
f=sub.add_parser('fetch');f.add_argument('image');f.add_argument('output',type=pathlib.Path)
b=sub.add_parser('pack');b.add_argument('root',type=pathlib.Path);b.add_argument('output',type=pathlib.Path)
i=sub.add_parser('import-docker');i.add_argument('archive',type=pathlib.Path);i.add_argument('output',type=pathlib.Path)
e=sub.add_parser('extract-file');e.add_argument('layout',type=pathlib.Path);e.add_argument('member');e.add_argument('output',type=pathlib.Path)
a=p.parse_args();assert pathlib.Path('/work/field-edge').is_dir() and os.uname().sysname=='Linux'
def descriptor(blob,media,root):
 raw=blob if isinstance(blob,bytes) else json.dumps(blob,separators=(',',':')).encode();digest=hashlib.sha256(raw).hexdigest();(root/'blobs/sha256'/digest).write_bytes(raw)
 return {'mediaType':media,'digest':'sha256:'+digest,'size':len(raw)}
def finish(root,desc):
 desc['platform']={'architecture':'amd64','os':'linux'}
 (root/'index.json').write_text(json.dumps({'schemaVersion':2,'manifests':[desc]}));(root/'oci-layout').write_text('{"imageLayoutVersion":"1.0.0"}')
if a.command=='fetch':
 registry,name=a.image.split('/',1);name,reference=name.split('@',1) if '@' in name else name.rsplit(':',1)
 assert '/' in name and registry in ['ghcr.io','factory.talos.dev','registry-1.docker.io']
 root=a.output;assert str(root).startswith('/work/field-edge/');(root/'blobs/sha256').mkdir(parents=True,exist_ok=True)
 token=None
 def request(path):
  global token
  headers={'Accept':', '.join(['application/vnd.oci.image.index.v1+json','application/vnd.oci.image.manifest.v1+json','application/vnd.docker.distribution.manifest.list.v2+json','application/vnd.docker.distribution.manifest.v2+json'])}
  if token:headers['Authorization']='Bearer '+token
  url=f'https://{registry}/v2/{name}/{path}'
  try:return urllib.request.urlopen(urllib.request.Request(url,headers=headers),timeout=90)
  except urllib.error.HTTPError as exc:
   if exc.code!=401:raise
   challenge=exc.headers['WWW-Authenticate'];assert challenge.startswith('Bearer ')
   params=dict(re.findall(r'(\w+)="([^"]*)"',challenge));realm=params.pop('realm');assert realm.startswith('https://')
   auth=json.load(urllib.request.urlopen(realm+'?'+urllib.parse.urlencode(params),timeout=30));token=auth.get('token',auth.get('access_token'));assert token
   headers['Authorization']='Bearer '+token
   return urllib.request.urlopen(urllib.request.Request(url,headers=headers),timeout=90)
 with request('manifests/'+reference) as response:raw=response.read()
 if reference.startswith('sha256:'):assert hashlib.sha256(raw).hexdigest()==reference[7:]
 outer=json.loads(raw);outer_digest='sha256:'+hashlib.sha256(raw).hexdigest()
 if 'manifests' in outer:
  selected=next(m for m in outer['manifests'] if m.get('platform',{}).get('architecture')=='amd64' and m['platform'].get('os')=='linux')
  with request('manifests/'+selected['digest']) as response:raw=response.read()
  assert 'sha256:'+hashlib.sha256(raw).hexdigest()==selected['digest']
 manifest=json.loads(raw);desc=descriptor(raw,manifest['mediaType'],root)
 for blob in [manifest['config'],*manifest['layers']]:
  algorithm,digest=blob['digest'].split(':');assert algorithm=='sha256'
  path=root/'blobs/sha256'/digest
  if path.exists() and hashlib.file_digest(path.open('rb'),'sha256').hexdigest()==digest:continue
  tmp=path.with_suffix('.partial');h=hashlib.sha256();size=0
  with request('blobs/'+blob['digest']) as response,tmp.open('wb') as out:
   while chunk:=response.read(1024*1024):out.write(chunk);h.update(chunk);size+=len(chunk)
  assert h.hexdigest()==digest and size==blob['size'];tmp.replace(path)
 finish(root,desc);pin={'requested':a.image,'index_digest':outer_digest,'amd64_manifest_digest':desc['digest'],'config':manifest['config'],'layers':manifest['layers']}
 (root/'pin.json').write_text(json.dumps(pin,indent=2));print(json.dumps(pin))
elif a.command=='pack':
 root=a.output;assert str(root).startswith('/work/field-edge/');(root/'blobs/sha256').mkdir(parents=True,exist_ok=True)
 tmp=root/'layer.tar'
 with tarfile.open(tmp,'w') as tar:
  for path in sorted(a.root.rglob('*')):
   assert path.is_file() or path.is_dir(), 'extensions must contain regular files/directories only'
   tar.add(path,arcname=str(path.relative_to(a.root)),recursive=False)
 raw=tmp.read_bytes();layer=descriptor(raw,'application/vnd.oci.image.layer.v1.tar',root);tmp.unlink()
 config=descriptor({'architecture':'amd64','os':'linux','config':{},'rootfs':{'type':'layers','diff_ids':[layer['digest']]}},'application/vnd.oci.image.config.v1+json',root)
 desc=descriptor({'schemaVersion':2,'mediaType':'application/vnd.oci.image.manifest.v1+json','config':config,'layers':[layer]},'application/vnd.oci.image.manifest.v1+json',root);finish(root,desc);print(json.dumps(desc))
elif a.command=='import-docker':
 root=a.output;assert str(root).startswith('/work/field-edge/');(root/'blobs/sha256').mkdir(parents=True,exist_ok=True)
 with tarfile.open(a.archive,'r:*') as tar:
  manifest=json.load(tar.extractfile('manifest.json'));assert len(manifest)==1
  spec=manifest[0];config_raw=tar.extractfile(spec['Config']).read();config_obj=json.loads(config_raw)
  config=descriptor(config_raw,'application/vnd.oci.image.config.v1+json',root);layers=[]
  for index,path in enumerate(spec['Layers']):
   raw=tar.extractfile(path).read();layer=descriptor(raw,'application/vnd.oci.image.layer.v1.tar',root)
   assert layer['digest']==config_obj['rootfs']['diff_ids'][index], 'Docker archive layer must be uncompressed and match its diffID'
   layers.append(layer)
 desc=descriptor({'schemaVersion':2,'mediaType':'application/vnd.oci.image.manifest.v1+json','config':config,'layers':layers},'application/vnd.oci.image.manifest.v1+json',root);finish(root,desc);print(json.dumps(desc))
else:
 layout=a.layout;desc=json.loads((layout/'index.json').read_text())['manifests'][0]
 manifest=json.loads((layout/'blobs/sha256'/desc['digest'][7:]).read_text());found=False
 for layer in manifest['layers']:
  with tarfile.open(layout/'blobs/sha256'/layer['digest'][7:],'r:*') as tar:
   for member in tar:
    if member.name.lstrip('./')==a.member.lstrip('/'):
     assert member.isfile();a.output.parent.mkdir(parents=True,exist_ok=True);a.output.write_bytes(tar.extractfile(member).read());a.output.chmod(member.mode);found=True
 if not found:raise SystemExit('requested regular file absent from image layers')
