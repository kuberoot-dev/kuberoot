#!/bin/bash
set -euo pipefail
export KUBECONFIG=/work/trust/admin.kubeconfig
ready=0
for attempt in $(seq 1 90); do
 if kubectl --server https://127.0.0.1:5010 --request-timeout=3s get bootentries -o json 2>/dev/null | python3 -c 'import json,sys; r=json.load(sys.stdin);sys.exit(not any(x["status"].get("booted") and x["status"].get("state")=="Good" and x["status"].get("release")=="0.1.1-router.field.2" for x in r["items"]))'; then ready=1;break;fi
 sleep 3
done
[[ $ready == 1 ]]
export KUBECONFIG=/work/cluster-r1.conf
kubectl patch safeguard default --type merge -p '{"spec":{"forwardingChecks":["http://10.0.2.2:8090/"]}}'
for attempt in $(seq 1 120); do [[ -f /work/www/kuberoot-0.1.1-router.field.2-noforward-amd64.tar.sig ]] && break; sleep 3; done
cat > /work/field/R2-fixed-noforward.sh <<'SCENE'
#!/bin/bash
source /work/field/lib.sh
show 'date -u +%FT%TZ'
show 'export KUBECONFIG=/work/cluster-r1.conf'
show 'kubectl get safeguard default -o jsonpath="{.spec.forwardingChecks}"; printf "\n"'
show 'python3 /work/field/R2-upgrade-trial.py 0.1.1-router.field.2-noforward --expect-reject'
show 'date -u +%FT%TZ'
SCENE
DEFER_RENDER=1 bash /work/field/capture.sh t-R2-kuberoot-after-forwarding-fix /work/field/R2-fixed-noforward.sh
