#!/bin/bash
# Follows hack/e2e/etcd-ha.sh installation/enrollment; no baseline disk reused.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
require_stand
: "${HA_ROOT:?}" "${ARTIFACT_DIR:?}" "${KUBEROOT_VERSION:?}"
mkdir -p "$HA_ROOT"
export KUBECONFIG=$HA_ROOT/trust/admin.kubeconfig
node_command() { local id=$1; shift; kubectl --server="https://127.0.0.1:25${id}00" --request-timeout=5s "$@"; }
wait_for() { local end=$(($(date +%s)+$1));shift; until "$@"; do test "$(date +%s)" -lt "$end" || return 1; sleep 5; done; }
installed() { grep -q 'reboot: Restarting system' "$HA_ROOT/n$1/serial.log"; }
for id in 1 2 3; do
 args='';test "$id" != 1 || args=kuberoot.datastore=etcd
 mkimage -arch amd64 -version "$KUBEROOT_VERSION" -artifacts "$ARTIFACT_DIR" -trust "$HA_ROOT/trust" -out "$HA_ROOT/media-$id.img" -args "kuberoot.nameserver=10.0.2.3 kuberoot.ip=eth1:10.79.0.1$id/24 $args"
 bash "$FIELD_SCRIPTS/ha-vm.sh" start "$id" "$HA_ROOT/media-$id.img"
done
for id in 1 2 3; do
 wait_for 600 node_command "$id" get disks
 node_command "$id" create -f - <<'YAML'
apiVersion: node.kuberoot.dev/v1alpha1
kind: Installation
metadata: {name: install}
spec: {disk: vdb, reboot: true}
YAML
done
for id in 1 2 3; do
 wait_for 900 installed "$id"
 bash "$FIELD_SCRIPTS/ha-vm.sh" stop "$id"
 cp "$HA_ROOT/n$id/serial.log" "$HA_ROOT/n$id/install-serial.log"
 bash "$FIELD_SCRIPTS/ha-vm.sh" start "$id"
 wait_for 600 node_command "$id" get bootentries
done
node_command 1 get kubeconfig admin -o jsonpath='{.kubeconfig}' > "$HA_ROOT/cluster.conf"
for id in 1 2 3; do
 cp "$HA_ROOT/cluster.conf" "$HA_ROOT/kubeconfig-$id"
 KUBECONFIG=$HA_ROOT/kubeconfig-$id kubectl config set-cluster kuberoot --server="https://127.0.0.1:26${id}43" --tls-server-name="10.79.0.1$id"
done
for id in 2 3; do
 name=$(node_command "$id" get osconfigs -o jsonpath='{.items[0].status.hostname}')
 node_command 1 create -f - <<YAML
apiVersion: node.kuberoot.dev/v1alpha1
kind: JoinTicket
metadata: {name: field-cp-$id}
spec: {nodeName: $name, role: ControlPlane, address: 10.79.0.1$id}
YAML
 for i in $(seq 1 60); do
  node_command 1 get jointicket "field-cp-$id" -o jsonpath='{.status.membership}' > "$HA_ROOT/membership-$id.private"
  test ! -s "$HA_ROOT/membership-$id.private" || break
  sleep 2
 done
 node_command "$id" apply -f "$HA_ROOT/membership-$id.private"
 for i in $(seq 1 180); do
  count=$(node_command 1 get etcdmembers -o json | python3 -c 'import json,sys;print(sum(not x["status"].get("learner",False) for x in json.load(sys.stdin)["items"]))')
  test "$count" != "$id" || break
  sleep 5
 done
 test "$count" = "$id"
done
for id in 1 2 3; do
 KUBECONFIG=$HA_ROOT/kubeconfig-$id kubectl wait --for=condition=Ready nodes --all --timeout=600s
 KUBECONFIG=$HA_ROOT/kubeconfig-$id kubectl get nodes -o json > "$HA_ROOT/nodes-$id.json"
done
node_command 1 get etcdmembers -o json > "$HA_ROOT/members-before.json"
