#!/bin/bash
# Fresh Ubuntu disks, actual three-server embedded etcd, private shared join token.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
require_stand
: "${HA_ROOT:?owned ha-k3s directory}"
[[ $HA_ROOT == /work/field-edge/ha-* ]]
mkdir -p "$HA_ROOT"
umask 077
openssl rand -hex 32 > "$HA_ROOT/join-token.private"
export HA_ROOT FIELD_ROOT
python3 - <<'PY'
import json,os,pathlib
root=pathlib.Path(os.environ['HA_ROOT']);field=pathlib.Path(os.environ['FIELD_ROOT'])
key=(field/'gateway/key.pub').read_text().strip();token=(root/'join-token.private').read_text().strip()
for i in range(1,4):
 d=root/f'n{i}';d.mkdir()
 config={'token':token,'disable':['traefik','servicelb'],'node-ip':f'10.79.0.1{i}','advertise-address':f'10.79.0.1{i}','flannel-iface':'peer0','tls-san':['127.0.0.1',f'10.79.0.1{i}']}
 if i==1:config['cluster-init']=True
 else:config['server']='https://10.79.0.11:6443'
 command='curl -fL --retry 3 -o /usr/local/bin/k3s https://github.com/k3s-io/k3s/releases/download/v1.37.2%2Bk3s1/k3s; echo "265b3ee6a3198ef14033c63ef46cbb32370d83795199fb6d1b01f21575eddbda  /usr/local/bin/k3s" | sha256sum -c; chmod +x /usr/local/bin/k3s; curl -fL --retry 3 -o /root/install-k3s.sh https://raw.githubusercontent.com/k3s-io/k3s/v1.37.2%2Bk3s1/install.sh; INSTALL_K3S_SKIP_DOWNLOAD=true sh /root/install-k3s.sh server'
 cloud={'hostname':f'field-k3s-ha-{i}','users':[{'name':'observer','sudo':'ALL=(ALL) NOPASSWD:ALL','shell':'/bin/bash','ssh_authorized_keys':[key]}],'write_files':[{'path':'/etc/rancher/k3s/config.yaml','permissions':'0600','content':json.dumps(config)}],'runcmd':[['bash','-euc',command]]}
 (d/'user-data').write_text('#cloud-config\n'+json.dumps(cloud))
 (d/'meta-data').write_text(f'instance-id: field-k3s-ha-{i}\nlocal-hostname: field-k3s-ha-{i}\n')
 network={'version':2,'ethernets':{'management0':{'match':{'macaddress':f'52:54:00:79:01:0{i}'},'set-name':'management0','dhcp4':True},'peer0':{'match':{'macaddress':f'52:54:00:79:00:0{i}'},'set-name':'peer0','addresses':[f'10.79.0.1{i}/24']}}}
 (d/'network-config').write_text(json.dumps(network))
PY
ssh_node() { local id=$1;shift;ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="$HA_ROOT/known_hosts" -o ConnectTimeout=5 -i "$FIELD_ROOT/gateway/key" -p "22${id}22" observer@127.0.0.1 "$@"; }
for id in 1 2 3; do
 d=$HA_ROOT/n$id
 genisoimage -quiet -output "$d/seed.iso" -volid cidata -joliet -rock "$d/user-data" "$d/meta-data" "$d/network-config"
 qemu-img convert -f qcow2 -O raw "$FIELD_ROOT/downloads/ubuntu-24.04-server-cloudimg-amd64.img" "$d/system.raw"
 qemu-img resize -f raw "$d/system.raw" 32G
 bash "$FIELD_SCRIPTS/ha-vm.sh" start "$id" "$d/seed.iso"
 for attempt in $(seq 1 180); do
  if ssh_node "$id" 'sudo test -f /etc/rancher/k3s/k3s.yaml && systemctl is-active k3s'; then break;fi
  sleep 5
 done
 ssh_node "$id" 'sudo cat /etc/rancher/k3s/k3s.yaml' > "$HA_ROOT/kubeconfig-$id"
 KUBECONFIG=$HA_ROOT/kubeconfig-$id kubectl config set-cluster default --server="https://127.0.0.1:26${id}43" --tls-server-name=127.0.0.1 >/dev/null
 KUBECONFIG=$HA_ROOT/kubeconfig-$id kubectl wait --for=condition=Ready "node/field-k3s-ha-$id" --timeout=600s
done
for id in 1 2 3; do
 KUBECONFIG=$HA_ROOT/kubeconfig-$id kubectl get nodes -o json > "$HA_ROOT/nodes-$id.json"
 ssh_node "$id" 'sudo curl --fail --silent --cacert /var/lib/rancher/k3s/server/tls/etcd/server-ca.crt --cert /var/lib/rancher/k3s/server/tls/etcd/client.crt --key /var/lib/rancher/k3s/server/tls/etcd/client.key https://127.0.0.1:2379/metrics' > "$HA_ROOT/etcd-metrics-$id.txt"
done
python3 - "$HA_ROOT" <<'PY'
import pathlib,re,sys,json
root=pathlib.Path(sys.argv[1]);leaders=[]
for i in range(1,4):
 text=(root/f'etcd-metrics-{i}.txt').read_text()
 if re.search(r'^etcd_server_is_leader(?:\{[^}]*\})? 1(?:\.0)?$',text,re.M):leaders.append(i)
if len(leaders)!=1:raise SystemExit('No unique observed etcd leader; inspect retained public metrics before E5')
(root/'observed-leader.json').write_text(json.dumps({'leader_node':leaders[0],'source':'etcd_server_is_leader, authenticated local metrics'}))
print('Observed etcd leader node',leaders[0])
PY
