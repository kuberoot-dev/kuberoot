#!/usr/bin/env python3
"""Read embedded etcd's real JSON gateway through a declared host-network Pod."""
import json,os,pathlib,subprocess
h=pathlib.Path(os.environ['HA_ROOT']);out=pathlib.Path(os.environ['CASE_OUTPUT'])
def kube(*args,**kw):return subprocess.run(['kubectl','--request-timeout=15s',*args],text=True,capture_output=True,check=True,**kw)
node=next(n['metadata']['name'] for n in json.loads(kube('get','nodes','-o','json').stdout)['items'] if any(a['address']=='10.79.0.11' for a in n['status']['addresses']))
code="import ssl,urllib.request; c=ssl.create_default_context(cafile='/identity/server-ca.crt'); c.load_cert_chain('/identity/client.crt','/identity/client.key'); print(urllib.request.urlopen(urllib.request.Request('https://127.0.0.1:2379/v3/cluster/member/list',data=b'{}',headers={'Content-Type':'application/json'}),context=c,timeout=10).read().decode())"
pod={'apiVersion':'v1','kind':'Pod','metadata':{'name':'membership-audit','namespace':'field'},'spec':{'hostNetwork':True,'nodeName':node,'restartPolicy':'Never','containers':[{'name':'audit','image':os.environ['PYTHON_IMAGE'],'command':['python3','-c',code],'volumeMounts':[{'name':'identity','mountPath':'/identity','readOnly':True}]}],'volumes':[{'name':'identity','hostPath':{'path':'/var/lib/rancher/k3s/server/tls/etcd','type':'Directory'}}]}}
kube('apply','-f','-',input=json.dumps(pod))
try:
 kube('-n','field','wait','--for=jsonpath={.status.phase}=Succeeded','pod/membership-audit','--timeout=120s');raw=kube('-n','field','logs','membership-audit').stdout;value=json.loads(raw);assert 'members' in value;(out/'members-after.json').write_text(json.dumps(value,indent=2));print(json.dumps(value))
finally:kube('-n','field','delete','pod','membership-audit','--wait=true')
