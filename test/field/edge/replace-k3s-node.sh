#!/bin/bash
# Replacement provisioned by cloud-init; no interactive/SSH repair commands.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
: "${HA_ROOT:?}" "${CASE_OUTPUT:?}" "${OLD_NODE_NAME:?}"
export KUBECONFIG=$HA_ROOT/kubeconfig-1
python3 "$FIELD_SCRIPTS/node-identity-probe.py" collect --product k3s
start=$(date +%s)
printf '%s\n' '1. Stop old node via QMP and retain its disk.' '2. Delete old Kubernetes Node (k3s etcd controller removes its member).' '3. Provision blank replacement from original Ubuntu cloud image and declared cloud-init.' '4. Reuse the existing cluster join token; no claim of token rotation.' '5. Verify new Node Ready and actual old kubelet certificate authentication.' > "$CASE_OUTPUT/replacement-actions.txt"
cat "$CASE_OUTPUT/replacement-actions.txt"
bash "$FIELD_SCRIPTS/ha-vm.sh" stop 3
mv "$HA_ROOT/n3" "$HA_ROOT/n3-retired"
kubectl delete node "$OLD_NODE_NAME"
mkdir "$HA_ROOT/n3"
python3 - <<'PY'
import json,os,pathlib
h=pathlib.Path(os.environ['HA_ROOT']);old=h/'n3-retired';new=h/'n3';cloud=json.loads((old/'user-data').read_text().removeprefix('#cloud-config\n'));cloud['hostname']='field-k3s-replacement'
(new/'user-data').write_text('#cloud-config\n'+json.dumps(cloud));(new/'meta-data').write_text('instance-id: field-k3s-replacement\nlocal-hostname: field-k3s-replacement\n')
net=json.loads((old/'network-config').read_text());net['ethernets']['management0']['match']['macaddress']='52:54:00:79:02:03';net['ethernets']['peer0']['match']['macaddress']='52:54:00:79:03:03';(new/'network-config').write_text(json.dumps(net))
PY
d=$HA_ROOT/n3
genisoimage -quiet -output "$d/seed.iso" -volid cidata -joliet -rock "$d/user-data" "$d/meta-data" "$d/network-config"
qemu-img convert -f qcow2 -O raw "$FIELD_ROOT/downloads/ubuntu-24.04-server-cloudimg-amd64.img" "$d/system.raw"
qemu-img resize -f raw "$d/system.raw" 32G
HA_MANAGEMENT_MAC=52:54:00:79:02:03 HA_PEER_MAC=52:54:00:79:03:03 bash "$FIELD_SCRIPTS/ha-vm.sh" start 3
for i in $(seq 1 120);do kubectl get node field-k3s-replacement && break;sleep 5;done
kubectl wait --for=condition=Ready node/field-k3s-replacement --timeout=600s
end=$(date +%s)
python3 "$FIELD_SCRIPTS/node-identity-probe.py" probe --product k3s
# Authenticated datastore membership via a declared diagnostic workload, no SSH.
python3 "$FIELD_SCRIPTS/k3s-membership-audit.py"
python3 - "$start" "$end" <<'PY'
import json,os,pathlib,sys
p=pathlib.Path(os.environ['CASE_OUTPUT']);members=json.loads((p/'members-after.json').read_text())['members'];names=[m.get('name','') for m in members];probe=json.loads((p/'old-identity-probe.json').read_text())
r={'replacement_ready_seconds':int(sys.argv[2])-int(sys.argv[1]),'replacement_name':'field-k3s-replacement','old_identity_revoked':probe['old_identity_revoked'],'no_duplicate_member':len(names)==3 and len(set(names))==3 and not any(n.startswith(os.environ['OLD_NODE_NAME']) for n in names) and any(n.startswith('field-k3s-replacement') for n in names),'member_names':names,'operator_actions':(p/'replacement-actions.txt').read_text().splitlines(),'credential_types_used':['Kubernetes administrator certificate','existing shared k3s server join token in cloud-init'],'old_disk_preserved':True,'retired_disk_directory':str(pathlib.Path(os.environ['HA_ROOT'])/'n3-retired'),'join_token_rotated':False}
(p/'replacement.json').write_text(json.dumps(r,indent=2));print(json.dumps(r))
PY
