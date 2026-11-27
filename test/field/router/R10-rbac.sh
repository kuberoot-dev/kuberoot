#!/bin/bash
source /work/field/lib.sh
export KUBECONFIG=/work/cluster-r1.conf
show 'date -u +%FT%TZ'
show 'curl -ks -o /work/field/raw/R10-anonymous-node.json -w "node API HTTP %{http_code}\n" https://127.0.0.1:5010/apis/node.kuberoot.dev/v1alpha1/osconfigs'
show 'curl -ks -o /work/field/raw/R10-anonymous-kube.json -w "kube API HTTP %{http_code}\n" https://127.0.0.1:6413/apis/router.kuberoot.dev/v1alpha1/interfaces'
show 'kubectl create clusterrole field-router-editor --verb=get,list,watch,create,update,patch,delete --resource=routes.router.kuberoot.dev,interfaces.router.kuberoot.dev --dry-run=client -o yaml | kubectl apply -f -'
show 'kubectl create clusterrolebinding field-router-editor --clusterrole=field-router-editor --user=field-router-editor --dry-run=client -o yaml | kubectl apply -f -'
show 'kubectl auth can-i create upgrades.node.kuberoot.dev --as field-router-editor || test $? = 1'
show 'kubectl patch interface lan --type merge -p '\''{"spec":{"mtu":1450}}'\'' --as article-reader 2>&1 || test $? = 1'
show 'kubectl get interface lan -o jsonpath="{.spec}"; printf "\n"'
show 'date -u +%FT%TZ'
