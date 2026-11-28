#!/bin/bash
# Credentials for r2 from its console (kuberoot.dev prints the node-admin
# kubeconfig) and the cluster admin kubeconfig from its node API.
set -euo pipefail
mkdir -p /work/trust
for _ in $(seq 120); do grep -q 'END KUBEROOT KUBECONFIG' /work/vms/r2/console.log 2>/dev/null && break; sleep 3; done
awk '/BEGIN KUBEROOT KUBECONFIG/{f=1;next} /END KUBEROOT KUBECONFIG/{f=0} f' /work/vms/r2/console.log | tail -1 | tr -d '\r' | base64 -d > /work/trust/admin.kubeconfig
c=$(kubectl --kubeconfig /work/trust/admin.kubeconfig config view -o jsonpath='{.clusters[0].name}')
kubectl --kubeconfig /work/trust/admin.kubeconfig config set-cluster "$c" --server=https://127.0.0.1:5020 >/dev/null
for _ in $(seq 100); do KUBECONFIG=/work/trust/admin.kubeconfig kubectl --request-timeout=5s get kubeconfig admin -o jsonpath='{.kubeconfig}' > /work/cluster-r2.conf 2>/dev/null && [ -s /work/cluster-r2.conf ] && break; sleep 3; done
c=$(kubectl --kubeconfig /work/cluster-r2.conf config view -o jsonpath='{.clusters[0].name}')
kubectl --kubeconfig /work/cluster-r2.conf config set-cluster "$c" --server=https://127.0.0.1:6423 --tls-server-name=10.255.0.2 >/dev/null
for _ in $(seq 100); do kubectl --kubeconfig /work/cluster-r2.conf --request-timeout=5s get --raw /readyz >/dev/null 2>&1 && break; sleep 3; done
KUBECONFIG=/work/trust/admin.kubeconfig kubectl get osconfigs,nodeservices
