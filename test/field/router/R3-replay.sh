#!/bin/bash
source /work/field/lib.sh
export KUBECONFIG=/work/cluster-r1.conf
show 'date -u +%FT%TZ'
show 'kubectl patch safeguard default --type merge -p '\''{"spec":{"confirmWithin":"60s"}}'\'''
cat > /work/field/bad-replay-route.yaml <<'YAML'
apiVersion: router.kuberoot.dev/v1alpha1
kind: Route
metadata: {name: field-gitops-bad-route}
spec: {destination: 1.1.1.1/32, gateway: 10.255.0.99, link: eth2}
YAML
show 'cat /work/field/bad-replay-route.yaml'
show 'for trial in $(seq 1 13); do date -u +%FT%TZ; kubectl apply -f /work/field/bad-replay-route.yaml; kubectl get safeguard default; sleep 10; done'
show 'kubectl get route field-gitops-bad-route -o yaml'
show 'kubectl delete route field-gitops-bad-route'
show 'date -u +%FT%TZ'
