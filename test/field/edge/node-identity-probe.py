#!/usr/bin/env python3
"""Save a real kubelet credential privately, then test its post-removal identity.
Collection uses a declared diagnostic Pod and Python, never a guest SSH shell.
Only SelfSubjectReview identity/status is public; certificate/key remain private.
"""
import argparse,base64,json,os,pathlib,subprocess
p=argparse.ArgumentParser();p.add_argument('action',choices=['collect','probe']);p.add_argument('--product',required=True);a=p.parse_args()
h=pathlib.Path(os.environ['HA_ROOT']);out=pathlib.Path(os.environ['CASE_OUTPUT']);name=os.environ['OLD_NODE_NAME'];private=h/'old-kubelet.private.json'
def kube(*args,**kw):return subprocess.run(['kubectl','--request-timeout=15s',*args],text=True,capture_output=True,check=True,**kw)
if a.action=='collect':
 path='/var/lib/rancher/k3s/agent' if a.product=='k3s' else '/var/lib/kubelet/pki'
 code="import pathlib,json,base64; p=pathlib.Path('/identity'); cert=p/'client-kubelet.crt'; key=p/'client-kubelet.key'; pair=next(iter(sorted((x for x in p.glob('kubelet-client-*.pem') if not x.is_symlink()),key=lambda x:x.stat().st_mtime,reverse=True)),p/'kubelet-client-current.pem'); print(json.dumps({'cert':base64.b64encode((cert if cert.exists() else pair).read_bytes()).decode(),'key':base64.b64encode((key if key.exists() else pair).read_bytes()).decode()}))"
 pod={'apiVersion':'v1','kind':'Pod','metadata':{'name':'identity-audit','namespace':'field'},'spec':{'nodeName':name,'restartPolicy':'Never','containers':[{'name':'audit','image':os.environ['PYTHON_IMAGE'],'command':['python3','-c','import time; time.sleep(900)'],'volumeMounts':[{'name':'identity','mountPath':'/identity','readOnly':True}]}],'volumes':[{'name':'identity','hostPath':{'path':path,'type':'Directory'}}]}}
 kube('apply','-f','-',input=json.dumps(pod));kube('-n','field','wait','--for=condition=Ready','pod/identity-audit','--timeout=180s')
 try:
  value=json.loads(kube('-n','field','exec','identity-audit','--','python3','-c',code).stdout)
  config=json.loads(kube('config','view','--raw','--minify','-o','json').stdout)
  config['users']=[{'name':'old-kubelet','user':{'client-certificate-data':value['cert'],'client-key-data':value['key']}}]
  for context in config['contexts']:context['context']['user']='old-kubelet'
  private.write_text(json.dumps(config));private.chmod(0o600)
  request={'apiVersion':'authentication.k8s.io/v1','kind':'SelfSubjectReview'}
  baseline=subprocess.run(['kubectl','--kubeconfig',str(private),'--request-timeout=10s','create','--raw','/apis/authentication.k8s.io/v1/selfsubjectreviews','-f','-'],input=json.dumps(request),text=True,capture_output=True)
  identity=json.loads(baseline.stdout).get('status',{}).get('userInfo',{}) if baseline.returncode==0 else None
  evidence={'old_node':name,'identity':identity,'exit_code':baseline.returncode,'diagnosis':baseline.stderr}
  (out/'old-identity-before.json').write_text(json.dumps(evidence,indent=2))
  assert identity and identity.get('username')=='system:node:'+name,'old kubelet identity was not authenticated before removal'
 finally:kube('-n','field','delete','pod','identity-audit','--wait=true')
 print('Saved old kubelet client credential privately via diagnostic Pod; no key material published.')
else:
 request={'apiVersion':'authentication.k8s.io/v1','kind':'SelfSubjectReview'}
 r=subprocess.run(['kubectl','--kubeconfig',str(private),'--request-timeout=10s','create','--raw','/apis/authentication.k8s.io/v1/selfsubjectreviews','-f','-'],input=json.dumps(request),text=True,capture_output=True)
 identity=None
 if r.returncode==0:identity=json.loads(r.stdout).get('status',{}).get('userInfo',{})
 # An unavailable server or authorization-denied response is not proof of revocation.
 baseline=json.loads((out/'old-identity-before.json').read_text())
 assert baseline['identity']['username']=='system:node:'+name
 # Certificate expiry text can concern the SERVER certificate, so it cannot
 # establish rejection/revocation of the retained client credential.
 revoked=r.returncode!=0 and 'Unauthorized' in r.stderr
 evidence={'old_node':name,'credential':'actual pre-removal kubelet client certificate','identity':identity,'exit_code':r.returncode,'diagnosis':r.stderr,'old_identity_revoked':revoked}
 (out/'old-identity-probe.json').write_text(json.dumps(evidence,indent=2));print(json.dumps(evidence))
