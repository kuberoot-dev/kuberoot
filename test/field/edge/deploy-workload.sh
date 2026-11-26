#!/bin/bash
set -euo pipefail
# PYTHON_IMAGE must be an immutable pull digest resolved during preparation.
: "${KUBECONFIG:?}" "${PYTHON_IMAGE:?python image pinned by digest}"
[[ $PYTHON_IMAGE == *@sha256:* ]] || exit 2
S=$(cd "$(dirname "$0")" && pwd)
kubectl create namespace field --dry-run=client -o yaml | kubectl apply -f -
kubectl -n field create configmap workload --from-file=workload.py="$S/workload.py" --dry-run=client -o yaml | kubectl apply -f -
node=$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}')
python3 - "$PYTHON_IMAGE" "$node" <<'PY' | kubectl apply -f -
import json,sys
image=sys.argv[1]
items=[{'apiVersion':'v1','kind':'PersistentVolume','metadata':{'name':'field-records'},'spec':{
 'capacity':{'storage':'1Gi'},'accessModes':['ReadWriteOnce'],'persistentVolumeReclaimPolicy':'Retain','storageClassName':'',
 'hostPath':{'path':'/var/lib/field-records','type':'DirectoryOrCreate'},
 'nodeAffinity':{'required':{'nodeSelectorTerms':[{'matchExpressions':[{'key':'kubernetes.io/hostname','operator':'In','values':[sys.argv[2]]}]}]}}}},
 {'apiVersion':'v1','kind':'PersistentVolumeClaim','metadata':{'name':'records','namespace':'field'},'spec':{'storageClassName':'','volumeName':'field-records','accessModes':['ReadWriteOnce'],'resources':{'requests':{'storage':'1Gi'}}}}]
for name,replicas,port in [('http',10,30080),('append',1,30081)]:
    container={'name':name,'image':image,'command':['python3','/code/workload.py'],
        'ports':[{'containerPort':8080}], 'resources':{'requests':{'cpu':'5m','memory':'16Mi'},'limits':{'memory':'96Mi'}},
        'volumeMounts':[{'name':'code','mountPath':'/code'}],
        'readinessProbe':{'httpGet':{'path':'/http','port':8080},'periodSeconds':2}}
    volumes=[{'name':'code','configMap':{'name':'workload'}}]
    if name=='append':
        container['command']+=['--records','/data/records']
        container['volumeMounts'].append({'name':'data','mountPath':'/data'})
        volumes.append({'name':'data','persistentVolumeClaim':{'claimName':'records'}})
    items.append({'apiVersion':'apps/v1','kind':'Deployment','metadata':{'name':name,'namespace':'field'},'spec':{'replicas':replicas,'selector':{'matchLabels':{'app':name}},'template':{'metadata':{'labels':{'app':name}},'spec':{'containers':[container],'volumes':volumes}}}})
    items.append({'apiVersion':'v1','kind':'Service','metadata':{'name':name,'namespace':'field'},'spec':{'type':'NodePort','selector':{'app':name},'ports':[{'port':8080,'targetPort':8080,'nodePort':port}]}})
print(json.dumps({'apiVersion':'v1','kind':'List','items':items}))
PY
kubectl -n field rollout status deployment/http --timeout=300s
kubectl -n field rollout status deployment/append --timeout=300s
kubectl -n field get pods -o wide
