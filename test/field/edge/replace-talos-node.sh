#!/bin/bash
# Native Talos API replacement; old machine disk is quarantined, never wiped.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
: "${HA_ROOT:?}" "${CASE_OUTPUT:?}" "${OLD_NODE_NAME:?}"
export KUBECONFIG=$HA_ROOT/kubeconfig-1 TALOSCONFIG=$HA_ROOT/config/talosconfig
python3 "$FIELD_SCRIPTS/node-identity-probe.py" collect --product talos
start=$(date +%s)
printf '%s\n' '1. Read etcd membership via Talos machine API.' '2. Stop old node via QMP, retain disk, remove its etcd member via Talos API.' '3. Delete the old Kubernetes Node.' '4. Boot new blank disk with new MAC from pinned Talos ISO.' '5. Apply replacement machine configuration via maintenance API.' '6. Verify Ready, datastore membership and old kubelet certificate authentication.' > "$CASE_OUTPUT/replacement-actions.txt"
cat "$CASE_OUTPUT/replacement-actions.txt"
talosctl --endpoints 127.0.0.1:25100 --nodes 10.79.0.11 etcd members > "$CASE_OUTPUT/members-before.txt"
member=$(python3 - "$CASE_OUTPUT/members-before.txt" "$OLD_NODE_NAME" <<'PY'
import sys
rows=[x.split() for x in open(sys.argv[1]) if sys.argv[2] in x.split()]
assert len(rows)==1,rows
# Native table: NODE, ID, HOSTNAME, PEER URLS, CLIENT URLS, LEARNER.
r=rows[0];i=r.index(sys.argv[2]);assert i>=1;print(r[i-1])
PY
)
bash "$FIELD_SCRIPTS/ha-vm.sh" stop 3
mv "$HA_ROOT/n3" "$HA_ROOT/n3-retired"
talosctl --endpoints 127.0.0.1:25100 --nodes 10.79.0.11 etcd remove-member "$member"
kubectl delete node "$OLD_NODE_NAME"
python3 "$FIELD_SCRIPTS/talos-ha-patch.py" 3 | sed -e 's/field-talos-ha-3/field-talos-replacement/' -e 's/52:54:00:79:00:03/52:54:00:79:03:03/' > "$HA_ROOT/network-replacement.yaml"
talosctl machineconfig patch "$HA_ROOT/config/controlplane.yaml" --patch @"$HA_ROOT/network-replacement.yaml" --output "$HA_ROOT/replacement.private.yaml" > "$HA_ROOT/replacement-patch.log" 2>&1
talosctl validate --config "$HA_ROOT/replacement.private.yaml" --mode metal
HA_MANAGEMENT_MAC=52:54:00:79:02:03 HA_PEER_MAC=52:54:00:79:03:03 HA_BOOT_ISO=$FIELD_ROOT/downloads/talos-v1.14.7-metal-amd64.iso bash "$FIELD_SCRIPTS/ha-vm.sh" start 3
for i in $(seq 1 120);do
 if talosctl --nodes 127.0.0.1:25300 apply-config --insecure --file "$HA_ROOT/replacement.private.yaml";then break;fi
 sleep 3
done
for i in $(seq 1 120);do kubectl get node field-talos-replacement && break;sleep 5;done
kubectl wait --for=condition=Ready node/field-talos-replacement --timeout=600s
end=$(date +%s)
talosctl --endpoints 127.0.0.1:25100 --nodes 10.79.0.11 etcd members > "$CASE_OUTPUT/members-after.txt"
python3 "$FIELD_SCRIPTS/node-identity-probe.py" probe --product talos
python3 - "$start" "$end" <<'PY'
import json,os,pathlib,sys
p=pathlib.Path(os.environ['CASE_OUTPUT']);text=(p/'members-after.txt').read_text();rows=[r.split() for r in text.splitlines() if r.strip() and not r.startswith('NODE')];names=[r[2] for r in rows];probe=json.loads((p/'old-identity-probe.json').read_text())
r={'replacement_ready_seconds':int(sys.argv[2])-int(sys.argv[1]),'replacement_name':'field-talos-replacement','old_identity_revoked':probe['old_identity_revoked'],'no_duplicate_member':len(names)==3 and len(set(names))==3 and os.environ['OLD_NODE_NAME'] not in names and 'field-talos-replacement' in names,'member_names':names,'operator_actions':(p/'replacement-actions.txt').read_text().splitlines(),'credential_types_used':['Talos administrator certificate','Kubernetes administrator certificate','existing cluster secrets in replacement machine config'],'old_disk_preserved':True,'retired_disk_directory':str(pathlib.Path(os.environ['HA_ROOT'])/'n3-retired'),'cluster_ca_rotated':False,'maintenance_apply_insecure':'Native initial unconfigured machine API only; enrolled API uses authenticated TLS.'}
(p/'replacement.json').write_text(json.dumps(r,indent=2));print(json.dumps(r))
PY
