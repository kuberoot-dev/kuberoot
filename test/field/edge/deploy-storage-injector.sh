#!/bin/bash
# Runs only inside the DUT through its Kubernetes API; never mounts workshop PVC.
set -euo pipefail
: "${KUBECONFIG:?}" "${PYTHON_IMAGE:?}"
S=$(cd "$(dirname "$0")" && pwd)
kubectl -n kube-system create configmap field-storage-injector --from-file=storage-stressor.py="$S/storage-stressor.py" --dry-run=client -o yaml | kubectl apply -f -
python3 - "$PYTHON_IMAGE" <<'PY' | kubectl apply -f -
import json,sys
pod={'apiVersion':'v1','kind':'Pod','metadata':{'name':'field-storage-injector','namespace':'kube-system','labels':{'app':'field-storage-injector'}},'spec':{'priorityClassName':'system-node-critical','tolerations':[{'operator':'Exists'}],'containers':[{'name':'injector','image':sys.argv[1],'command':['python3','/code/storage-stressor.py'],'resources':{'requests':{'cpu':'1m','memory':'32Mi'},'limits':{'memory':'64Mi'}},'volumeMounts':[{'name':'code','mountPath':'/code'},{'name':'state','mountPath':'/state'}]}],'volumes':[{'name':'code','configMap':{'name':'field-storage-injector'}},{'name':'state','hostPath':{'path':'/var/lib/field-storage-fault','type':'DirectoryOrCreate'}}]}}
service={'apiVersion':'v1','kind':'Service','metadata':{'name':'field-storage-injector','namespace':'kube-system'},'spec':{'type':'NodePort','selector':{'app':'field-storage-injector'},'ports':[{'port':8081,'targetPort':8081,'nodePort':30082}]}}
print(json.dumps({'apiVersion':'v1','kind':'List','items':[pod,service]}))
PY
kubectl -n kube-system wait --for=condition=Ready pod/field-storage-injector --timeout=300s
