#!/bin/bash
# Real node-API replacement; old disk and client identity retained for audit.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
: "${HA_ROOT:?}" "${CASE_OUTPUT:?}" "${OLD_NODE_NAME:?}" "${ARTIFACT_DIR:?}" "${KUBEROOT_VERSION:?}"
export KUBECONFIG=$HA_ROOT/kubeconfig-1
python3 "$FIELD_SCRIPTS/node-identity-probe.py" collect --product kuberoot
node_command() { local id=$1;shift; KUBECONFIG=$HA_ROOT/trust/admin.kubeconfig kubectl --server="https://127.0.0.1:25${id}00" --request-timeout=10s "$@"; }
wait_for() { local end=$(($(date +%s)+$1));shift;until "$@";do test "$(date +%s)" -lt "$end" || return 1;sleep 3;done; }
start=$(date +%s)
printf '%s\n' '1. Stop failed node via QMP; retain old disk and UEFI vars.' '2. Delete old etcd member via node API.' '3. Delete old Kubernetes Node.' '4. Boot blank 32 GiB disk with new MAC and trusted installation media.' '5. Create Installation via node API.' '6. Request new ControlPlane JoinTicket for new hostname.' '7. Apply returned Membership privately to replacement node API.' '8. Verify Ready, membership and old client certificate authentication.' > "$CASE_OUTPUT/replacement-actions.txt"
cat "$CASE_OUTPUT/replacement-actions.txt"
bash "$FIELD_SCRIPTS/ha-vm.sh" stop 3
mv "$HA_ROOT/n3" "$HA_ROOT/n3-retired"
node_command 1 delete etcdmember "$OLD_NODE_NAME"
kubectl delete node "$OLD_NODE_NAME"
export HA_MANAGEMENT_MAC=52:54:00:79:02:03 HA_PEER_MAC=52:54:00:79:03:03
mkimage -arch amd64 -version "$KUBEROOT_VERSION" -artifacts "$ARTIFACT_DIR" -trust "$HA_ROOT/trust" -out "$HA_ROOT/replacement.img" -args 'kuberoot.nameserver=10.0.2.3 kuberoot.ip=eth1:10.79.0.13/24'
bash "$FIELD_SCRIPTS/ha-vm.sh" start 3 "$HA_ROOT/replacement.img"
wait_for 300 node_command 3 get disks
node_command 3 create -f - <<'YAML'
apiVersion: node.kuberoot.dev/v1alpha1
kind: Installation
metadata: {name: replacement}
spec: {disk: vdb, reboot: true}
YAML
wait_for 600 grep -q 'reboot: Restarting system' "$HA_ROOT/n3/serial.log"
bash "$FIELD_SCRIPTS/ha-vm.sh" stop 3
cp "$HA_ROOT/n3/serial.log" "$HA_ROOT/n3/install-serial.log"
bash "$FIELD_SCRIPTS/ha-vm.sh" start 3
wait_for 300 node_command 3 get bootentries
name=$(node_command 3 get osconfigs -o jsonpath='{.items[0].status.hostname}')
test "$name" != "$OLD_NODE_NAME"
node_command 1 create -f - <<YAML
apiVersion: node.kuberoot.dev/v1alpha1
kind: JoinTicket
metadata: {name: field-replacement}
spec: {nodeName: $name, role: ControlPlane, address: 10.79.0.13}
YAML
for attempt in $(seq 1 60);do
 node_command 1 get jointicket field-replacement -o jsonpath='{.status.membership}' > "$HA_ROOT/replacement-membership.private"
 test ! -s "$HA_ROOT/replacement-membership.private" || break
 sleep 2
done
node_command 3 apply -f "$HA_ROOT/replacement-membership.private"
wait_for 600 kubectl get node "$name"
kubectl wait --for=condition=Ready "node/$name" --timeout=600s
end=$(date +%s)
node_command 1 get etcdmembers -o json > "$CASE_OUTPUT/members-after.json"
python3 "$FIELD_SCRIPTS/node-identity-probe.py" probe --product kuberoot
python3 - "$start" "$end" "$name" <<'PY'
import json,os,pathlib,sys
p=pathlib.Path(os.environ['CASE_OUTPUT']);members=json.loads((p/'members-after.json').read_text())['items'];names=[m['metadata']['name'] for m in members]
probe=json.loads((p/'old-identity-probe.json').read_text())
r={'replacement_ready_seconds':int(sys.argv[2])-int(sys.argv[1]),'replacement_name':sys.argv[3],'old_identity_revoked':probe['old_identity_revoked'],'no_duplicate_member':len(names)==3 and len(set(names))==3 and os.environ['OLD_NODE_NAME'] not in names and sys.argv[3] in names,'member_names':names,'operator_actions':(p/'replacement-actions.txt').read_text().splitlines(),'credential_types_used':['node API administrator certificate','Kubernetes administrator certificate','new ControlPlane JoinTicket Membership'],'replacement_management_mac':'52:54:00:79:02:03','replacement_peer_mac':'52:54:00:79:03:03','old_disk_preserved':True,'retired_disk_directory':str(pathlib.Path(os.environ['HA_ROOT'])/'n3-retired')}
(p/'replacement.json').write_text(json.dumps(r,indent=2));print(json.dumps(r))
PY
