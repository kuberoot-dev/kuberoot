#!/bin/bash
# R10 on r1: declared management exposure, port forward over the API port, API load while forwarding.
set -uo pipefail
source /work/field/lib.sh
export KUBECONFIG=/work/cluster-r1.conf
show 'kubectl get firewallzones -o custom-columns=NAME:.metadata.name,LINKS:.spec.links,INPUT:.spec.input,MANAGEMENT:.spec.management'
c1=$(kubectl get dhcpserver lan -o jsonpath='{.status.leases[0].address}')
show "kubectl get dhcpserver lan -o jsonpath='{.status.leases[*].address}'; echo"
# port forward on the WAN link that overlaps the API port; management zone must keep the API
show "kubectl apply -f - <<<'{\"apiVersion\":\"router.kuberoot.dev/v1alpha1\",\"kind\":\"NATRule\",\"metadata\":{\"name\":\"field-pf-6443\"},\"spec\":{\"portForward\":{\"inLink\":\"eth0\",\"protocol\":\"tcp\",\"port\":6443,\"to\":\"$c1\",\"toPort\":22}}}'"
sleep 5
show 'kubectl get natrule field-pf-6443 -o jsonpath="{.status.conditions[0].reason} {.status.conditions[0].message}"; echo'
show 'kubectl --request-timeout=5s get --raw /readyz; echo "  <- API over the WAN link while the port forward exists"'
show 'kubectl get safeguard default -o jsonpath="{.status.pending}"; echo'
rev=$(kubectl get safeguard default -o jsonpath='{.status.pending}'); [ -n "$rev" ] && kubectl patch safeguard default --type=merge -p "{\"spec\":{\"confirm\":\"$rev\"}}" >/dev/null
show 'kubectl delete natrule field-pf-6443'
sleep 5; rev=$(kubectl get safeguard default -o jsonpath='{.status.pending}'); [ -n "$rev" ] && kubectl patch safeguard default --type=merge -p "{\"spec\":{\"confirm\":\"$rev\"}}" >/dev/null
# 100 req/s to the API while forwarding is pinged from the transit side through r1 to c1
/work/field/guest-command gateway "nohup ping -D -i 0.1 -W 1 -w 70 $c1 > /root/R10-fwd.txt 2>&1 </dev/null &"
show 'python3 /work/field/R10-api-load.py'
sleep 12
show "/work/field/guest-command gateway 'tail -3 /root/R10-fwd.txt'"
/work/field/guest-command gateway 'cat /root/R10-fwd.txt' > /work/field/raw/R10-kuberoot-load-fwd.txt
