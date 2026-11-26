#!/bin/bash
source /work/field/lib.sh
export KUBECONFIG=/work/trust/admin.kubeconfig
show 'date -u +%FT%TZ'
show 'kubectl --server https://127.0.0.1:5010 get bootentries'
for mode in disconnect truncated; do
 cat > "/work/field/R2-$mode.yaml" <<YAML
apiVersion: node.kuberoot.dev/v1alpha1
kind: Upgrade
metadata: {name: field-download-$mode}
spec:
  url: http://10.0.2.2:8089/$mode/bundle.tar
  sha256: 813e8edd3111fa553db3a1118d978f777cc4e49af9166ca80ce87416955e8170
  reboot: true
YAML
 show "kubectl --server https://127.0.0.1:5010 apply -f /work/field/R2-$mode.yaml"
 for attempt in $(seq 1 30); do
  state=$(kubectl --server https://127.0.0.1:5010 get upgrade "field-download-$mode" -o jsonpath='{.status.phase}')
  [[ $state == Failed ]] && break
  sleep 2
 done
 show "kubectl --server https://127.0.0.1:5010 get upgrade field-download-$mode -o yaml"
done
show 'kubectl --server https://127.0.0.1:5010 get bootentries'
show 'date -u +%FT%TZ'
