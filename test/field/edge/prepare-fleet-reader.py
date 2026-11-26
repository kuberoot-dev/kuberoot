#!/usr/bin/env python3
"""Issue an ordinary non-admin client certificate and grant inventory read only."""
import base64,json,os,pathlib,subprocess,time
root=pathlib.Path('/work/field-edge/fleet-field');d=root/'restricted-user';d.mkdir(mode=0o700);env=dict(os.environ,KUBECONFIG=str(root/'center-kubeconfig'))
def kube(*args,body=None):return subprocess.check_output(['kubectl',*args],input=body,text=True,env=env)
subprocess.run(['openssl','req','-new','-newkey','ed25519','-nodes','-keyout',str(d/'client.key'),'-out',str(d/'client.csr'),'-subj','/CN=field-proxy-reader'],check=True,capture_output=True)
csr={'apiVersion':'certificates.k8s.io/v1','kind':'CertificateSigningRequest','metadata':{'name':'field-proxy-reader'},'spec':{'signerName':'kubernetes.io/kube-apiserver-client','request':base64.b64encode((d/'client.csr').read_bytes()).decode(),'usages':['client auth']}}
kube('create','-f','-',body=json.dumps(csr));kube('certificate','approve','field-proxy-reader')
for _ in range(90):
 cert=kube('get','csr','field-proxy-reader','-o','jsonpath={.status.certificate}')
 if cert:break
 time.sleep(1)
assert cert,'client certificate not issued'
objects=[{'apiVersion':'rbac.authorization.k8s.io/v1','kind':'ClusterRole','metadata':{'name':'field-inventory-reader'},'rules':[{'apiGroups':['fleet.kuberoot.dev'],'resources':['fleetnodes'],'verbs':['get','list']}]},{'apiVersion':'rbac.authorization.k8s.io/v1','kind':'ClusterRoleBinding','metadata':{'name':'field-inventory-reader'},'roleRef':{'apiGroup':'rbac.authorization.k8s.io','kind':'ClusterRole','name':'field-inventory-reader'},'subjects':[{'kind':'User','name':'field-proxy-reader','apiGroup':'rbac.authorization.k8s.io'}]}]
kube('create','-f','-',body=json.dumps({'apiVersion':'v1','kind':'List','items':objects}))
config=json.loads(kube('config','view','--raw','-o','json'));config['users']=[{'name':'field-proxy-reader','user':{'client-certificate-data':cert,'client-key-data':base64.b64encode((d/'client.key').read_bytes()).decode()}}]
for ctx in config['contexts']:ctx['context']['user']='field-proxy-reader'
p=d/'inventory-kubeconfig';p.touch(mode=0o600);p.write_text(json.dumps(config))
subprocess.run(['kubectl','get','fleetnodes'],env=dict(env,KUBECONFIG=str(p)),check=True,stdout=(d/'inventory-read.txt').open('w'))
fleetca=kube('-n','kube-system','get','configmap','kuberoot-fleet-ca','-o','jsonpath={.data.ca\\.crt}')
for c in config['clusters']:c['cluster']={'server':'https://127.0.0.1:17443/nodes/site-1','tls-server-name':'kuberoot-fleet','certificate-authority-data':base64.b64encode(fleetca.encode()).decode()}
p=d/'proxy-kubeconfig';p.touch(mode=0o600);p.write_text(json.dumps(config));print('Restricted certificate can read inventory; no fleetnodes/proxy permission granted.')
