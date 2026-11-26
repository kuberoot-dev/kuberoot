#!/usr/bin/env python3
"""Resolve and preserve a Docker Hub manifest digest; never use a mutable tag in W."""
import datetime,json,pathlib,urllib.request
root=pathlib.Path('/work/field-edge/downloads')
with urllib.request.urlopen('https://auth.docker.io/token?service=registry.docker.io&scope=repository:library/python:pull',timeout=30) as r: token=json.load(r)['token']
request=urllib.request.Request('https://registry-1.docker.io/v2/library/python/manifests/3.11-alpine',headers={'Authorization':'Bearer '+token,'Accept':'application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json'})
with urllib.request.urlopen(request,timeout=30) as r:
 manifest=r.read();digest=r.headers['Docker-Content-Digest']
assert digest.startswith('sha256:')
(root/'workload-manifest.json').write_bytes(manifest)
record={'resolved_utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'tag':'docker.io/library/python:3.11-alpine','image':'docker.io/library/python@'+digest,'scope':'W implementation image; default product baseline does not include W'}
(root/'workload-image.json').write_text(json.dumps(record,indent=2)+'\n');print(json.dumps(record,indent=2))
