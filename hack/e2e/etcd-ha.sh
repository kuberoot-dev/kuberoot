#!/usr/bin/env bash
# End-to-end test of a highly available control plane across an upgrade of
# its image, on three QEMU/KVM virtual machines:
#
#   1. three control planes installed from the old image, on etcd: the first
#      created with kuberoot.datastore=etcd, the others joined with
#      JoinTickets of role ControlPlane;
#   2. a rolling A/B Upgrade to the new image's bundle, one node at a time,
#      each waited for until its slot is Good and the store has three voters,
#      while a marker ConfigMap is written and read back every few seconds
#      through whichever API servers answer;
#   3. a rollback of one node to its old slot (BootEntry spec.preferred) and
#      forward again, with the node's store member and the marker intact;
#   4. a StateBackup of the first control plane, uploaded encrypted to an S3
#      endpoint, then the whole cluster lost and the first control plane
#      installed on a blank disk from the new image with
#      Installation.spec.restore: the marker comes back as it was when the
#      backup was taken, and a change made after it does not.
#
# Needs /dev/kvm, qemu-system-x86_64, OVMF, kubectl, go, age-keygen, rclone
# and python3. Every node's console log, the marker log and a summary stay
# in the stand directory.
#
# Usage: hack/e2e/etcd-ha.sh <old artifacts> <new artifacts> <stand dir>
#   artifacts: vmlinuz.efi, initrd.cpio, rootfs.squashfs, systemd-boot.efi,
#   VERSION (and kernel.args if any); the new ones also bundles/*.tar{,.sig}.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
OLD=$(cd "${1:?old artifacts required}" && pwd)
NEW=$(cd "${2:?new artifacts required}" && pwd)
V=${3:?stand directory required}
mkdir -p "$V"
V=$(cd "$V" && pwd)
OLD_VERSION=$(cat "$OLD/VERSION")
NEW_VERSION=$(cat "$NEW/VERSION")
BUNDLE=$(ls "$NEW"/bundles/*.tar | head -1)
MEM=${MEM:-3072}
SMP=${SMP:-2}
OVMF_CODE=${OVMF_CODE:-/usr/share/OVMF/OVMF_CODE_4M.fd}
OVMF_VARS=${OVMF_VARS:-/usr/share/OVMF/OVMF_VARS_4M.fd}
HTTP_PORT=8088
S3_PORT=9000

log() { echo "=== $(date -u +%H:%M:%S) $*" | tee -a "$V/summary.log"; }
fail() {
  log "FAIL: $*"
  exit 1
}
# until_ok <seconds> <command...>: retries the command every 5s.
until_ok() {
  local deadline=$(($(date +%s) + $1))
  shift
  until "$@" >/dev/null 2>&1; do
    [ "$(date +%s)" -lt $deadline ] || return 1
    sleep 5
  done
}

# --- virtual machines -------------------------------------------------------
# Node <i> is 10.10.0.1<i> on a shared L2 segment (eth1); its node API and
# cluster API are forwarded to 127.0.0.1:50<i>0 and 127.0.0.1:64<i>3 (eth0,
# user networking, which also reaches the host's HTTP and S3 at 10.0.2.2).

media() { # media <i> <artifacts> [kernel args]
  local i=$1 art=$2
  shift 2
  (cd "$ROOT" && go run ./cmd/mkimage -arch amd64 -artifacts "$art" -version "$(cat "$art/VERSION")" -trust "$V/trust" \
    -out "$V/media-$i.img" -args "kuberoot.nameserver=1.1.1.1 kuberoot.ip=eth1:10.10.0.1$i/24 $*") >/dev/null
}

up() { # up <i> [media image]
  local i=$1 m=${2:-} d=$V/n$1
  mkdir -p "$d"
  [ -f "$d/sys.qcow2" ] || qemu-img create -q -f qcow2 "$d/sys.qcow2" 20G
  [ -f "$d/vars.fd" ] || cp "$OVMF_VARS" "$d/vars.fd"
  local drives=()
  [ -z "$m" ] || drives+=(-drive "file=$m,format=raw,if=virtio,snapshot=on")
  drives+=(-drive "file=$d/sys.qcow2,format=qcow2,if=virtio")
  echo "=== boot $(date -u +%FT%TZ) media=${m:-none}" >>"$d/console.log"
  qemu-system-x86_64 -name "n$i" -enable-kvm -cpu host -smp "$SMP" -m "$MEM" -machine q35 \
    -drive if=pflash,format=raw,readonly=on,file="$OVMF_CODE" \
    -drive if=pflash,format=raw,file="$d/vars.fd" \
    "${drives[@]}" \
    -netdev user,id=n0,hostfwd=tcp:127.0.0.1:50${i}0-:50000,hostfwd=tcp:127.0.0.1:64${i}3-:6443 \
    -device virtio-net-pci,netdev=n0,mac=52:54:00:00:0$i:01 \
    -netdev socket,id=n1,mcast=230.0.0.1:12345,localaddr=127.0.0.1 \
    -device virtio-net-pci,netdev=n1,mac=52:54:00:01:0$i:01 \
    -chardev file,id=ser0,path="$d/console.log" -serial chardev:ser0 \
    -display none -daemonize -pidfile "$d/pid"
}

down() {
  local d=$V/n$1
  [ -f "$d/pid" ] && kill "$(cat "$d/pid")" 2>/dev/null || true
  for _ in $(seq 1 30); do
    [ -f "$d/pid" ] && kill -0 "$(cat "$d/pid")" 2>/dev/null || break
    sleep 1
  done
  rm -f "$d/pid"
}

# nk <i> args: kubectl against node <i>'s node API.
nk() {
  local i=$1
  shift
  kubectl --kubeconfig "$V/trust/admin.kubeconfig" --server "https://127.0.0.1:50${i}0" --request-timeout=20s "$@"
}
# ck <i> args: kubectl against node <i>'s API server, with the cluster's
# admin credentials.
ck() {
  local i=$1
  shift
  kubectl --kubeconfig "$V/cluster.conf" --server "https://127.0.0.1:64${i}3" --tls-server-name "10.10.0.1$i" --request-timeout=10s "$@"
}

booted() { # booted <i>: "<release> <state>" of the slot the node booted
  nk "$1" get bootentries -o jsonpath='{range .items[?(@.status.booted==true)]}{.status.release} {.status.state}{end}'
}
etcd_version() { # the version the node's etcd last started as
  nk "$1" get --raw /apis/node.kuberoot.dev/v1alpha1/nodeservices/etcd/log | grep -o '"etcd-version":"[^"]*"' | tail -1 | cut -d'"' -f4
}
voters() { # members of the store that vote
  nk 1 get etcdmembers -o jsonpath='{range .items[?(@.status.learner!=true)]}{.metadata.name}{"\n"}{end}' 2>/dev/null | grep -c . ||
    nk 2 get etcdmembers -o jsonpath='{range .items[?(@.status.learner!=true)]}{.metadata.name}{"\n"}{end}' | grep -c .
}
voters_are() { [ "$(voters)" = "$1" ]; }
three_voters() { voters_are 3; }
ticket_issued() { nk 1 get jointicket "$1" -o jsonpath='{.status.membership}' | grep -q Membership; }
rebooted() { grep -q 'reboot: Restarting system' "$V/n$1/console.log"; }
nodes_ready() { # every node of the cluster Ready, seen through node <i>'s API server
  local s
  s=$(ck "$1" get nodes -o jsonpath='{range .items[*]}{.status.conditions[?(@.type=="Ready")].status}{"\n"}{end}')
  [ "$(echo "$s" | grep -c True)" = 3 ] && ! echo "$s" | grep -qv True
}
slot_good() { [ "$(booted "$1")" = "$2 Good" ]; }

reboot_node() {
  local name
  name=$(nk "$1" get osconfigs -o jsonpath='{.items[0].metadata.name}')
  nk "$1" patch osconfig "$name" --type merge -p "{\"spec\":{\"rebootRequestedAt\":\"$(date -u +%FT%TZ)\"}}" >/dev/null
}

# --- marker -----------------------------------------------------------------
# Every two seconds: patch the marker through one API server, starting from
# a different one each time and taking the first that answers, then read it
# back through another. A round is good when both happened.
marker_loop() {
  set +e
  local n=0 w r i k v
  while [ ! -e "$V/marker.stop" ]; do
    n=$((n + 1))
    w="" r=""
    for k in 0 1 2; do
      i=$(((n + k) % 3 + 1))
      if ck "$i" -n default patch configmap e2e-marker --type merge -p "{\"data\":{\"n\":\"$n\"}}" >/dev/null 2>&1; then
        w=$i
        break
      fi
    done
    if [ -n "$w" ]; then
      for k in 1 2; do
        i=$(((w - 1 + k) % 3 + 1))
        v=$(ck "$i" -n default get configmap e2e-marker -o jsonpath='{.data.n}' 2>/dev/null) || continue
        [ "$v" = "$n" ] && r=$i && break
      done
    fi
    echo "$(date +%s) $n write=${w:--} read=${r:--}" >>"$V/marker.log"
    sleep 2
  done
}

marker_report() { # rounds, failed rounds and the longest time without a good round
  awk -v start="$1" '$1 >= start {
      n++; if ($3 == "write=-" || $4 == "read=-") bad++; else { if (last && $1 - last > gap) gap = $1 - last; last = $1 }
    } END { printf "%d %d %d\n", n, bad, gap }' "$V/marker.log"
}

# --- stand ------------------------------------------------------------------
cleanup() {
  touch "$V/marker.stop"
  for i in 1 2 3; do down "$i"; done
  [ -z "${HTTP_PID:-}" ] || kill "$HTTP_PID" 2>/dev/null || true
  [ -z "${S3_PID:-}" ] || kill "$S3_PID" 2>/dev/null || true
}
trap cleanup EXIT

rm -f "$V/summary.log" "$V/marker.log" "$V/marker.stop"
log "old image $OLD_VERSION, new image $NEW_VERSION ($(basename "$BUNDLE"))"
mkdir -p "$V/www" "$V/s3/backups"
cp "$BUNDLE" "$BUNDLE.sig" "$V/www/"
BUNDLE_URL=http://10.0.2.2:$HTTP_PORT/$(basename "$BUNDLE")
BUNDLE_SHA=$(sha256sum "$BUNDLE" | cut -d' ' -f1)
python3 -m http.server -d "$V/www" -b 127.0.0.1 $HTTP_PORT >"$V/http.log" 2>&1 &
HTTP_PID=$!
rclone serve s3 "$V/s3" --addr 127.0.0.1:$S3_PORT --auth-key e2e,e2e-secret-key >"$V/s3.log" 2>&1 &
S3_PID=$!

log "install three nodes from the old image"
media 1 "$OLD" kuberoot.datastore=etcd
media 2 "$OLD"
media 3 "$OLD"
for i in 1 2 3; do up "$i" "$V/media-$i.img"; done
for i in 1 2 3; do
  until_ok 600 nk "$i" get osconfigs || fail "node $i: no node API on the live system"
  nk "$i" create -f - <<EOF >/dev/null
apiVersion: node.kuberoot.dev/v1alpha1
kind: Installation
metadata: {name: install}
spec: {disk: vdb, reboot: true}
EOF
done
for i in 1 2 3; do
  until_ok 900 rebooted "$i" || fail "node $i: installation did not finish"
  down "$i"
  up "$i"
done
for i in 1 2 3; do
  until_ok 600 slot_good "$i" "$OLD_VERSION" || fail "node $i: did not boot its installed slot ($(booted "$i" || true))"
done
nk 1 get kubeconfig admin -o jsonpath='{.kubeconfig}' >"$V/cluster.conf"
until_ok 600 ck 1 get --raw /readyz || fail "node 1: cluster API not ready"
log "node 1: etcd $(etcd_version 1)"

for i in 2 3; do
  name=$(nk "$i" get osconfigs -o jsonpath='{.items[0].status.hostname}')
  log "join node $i ($name) as a control plane"
  nk 1 create -f - <<EOF >/dev/null
apiVersion: node.kuberoot.dev/v1alpha1
kind: JoinTicket
metadata: {name: cp-$i}
spec: {nodeName: $name, role: ControlPlane, address: 10.10.0.1$i}
EOF
  until_ok 60 ticket_issued "cp-$i" || fail "ticket cp-$i has no membership"
  nk 1 get jointicket "cp-$i" -o jsonpath='{.status.membership}' | nk "$i" apply -f - >/dev/null
  until_ok 900 voters_are "$i" || fail "node $i did not become a voting member"
done
for i in 1 2 3; do until_ok 900 nodes_ready "$i" || fail "nodes not Ready through node $i"; done
nk 1 get etcdmembers | tee -a "$V/summary.log"
for i in 1 2 3; do log "node $i: $(booted "$i"), etcd $(etcd_version "$i")"; done

ck 1 -n default create configmap e2e-marker --from-literal=n=0 >/dev/null
marker_loop &
MARKER_PID=$!
sleep 20

# --- rolling upgrade --------------------------------------------------------
roll_start=$(date +%s)
for i in 1 2 3; do
  log "upgrade node $i to $NEW_VERSION"
  nk "$i" create -f - <<EOF >/dev/null
apiVersion: node.kuberoot.dev/v1alpha1
kind: Upgrade
metadata: {name: to-new}
spec: {url: "$BUNDLE_URL", sha256: "$BUNDLE_SHA", reboot: true}
EOF
  until_ok 1200 slot_good "$i" "$NEW_VERSION" || fail "node $i: new slot not Good ($(booted "$i" || true))"
  until_ok 600 three_voters || fail "store lost a voter after node $i"
  until_ok 600 nodes_ready "$i" || fail "nodes not Ready after node $i"
  log "node $i: $(booted "$i"), etcd $(etcd_version "$i")"
done
sleep 20
read -r rounds bad gap <<<"$(marker_report "$roll_start")"
log "rolling upgrade: $rounds marker rounds, $bad failed, longest gap ${gap}s"
for i in 1 2 3; do [ "$(etcd_version "$i")" = "${ETCD_NEW:-3.7.3}" ] || fail "node $i runs etcd $(etcd_version "$i")"; done
[ "$gap" -le "${MAX_GAP:-30}" ] || fail "the cluster was unavailable for ${gap}s"

# --- rollback ---------------------------------------------------------------
member_id() { nk 1 get etcdmembers -o jsonpath="{range .items[?(@.metadata.name==\"$1\")]}{.status.id}{end}"; }
n3=$(nk 3 get osconfigs -o jsonpath='{.items[0].status.hostname}')
id_before=$(member_id "$n3")
old_slot=$(nk 3 get bootentries -o jsonpath='{range .items[?(@.status.booted!=true)]}{.metadata.name}{end}')
new_slot=$(nk 3 get bootentries -o jsonpath='{range .items[?(@.status.booted==true)]}{.metadata.name}{end}')
back_start=$(date +%s)
log "roll node 3 back to $old_slot ($OLD_VERSION), member $id_before"
nk 3 patch bootentry "$old_slot" --type merge -p '{"spec":{"preferred":true}}' >/dev/null
reboot_node 3
sleep 30
until_ok 900 slot_good 3 "$OLD_VERSION" || fail "node 3 did not come back on the old slot ($(booted 3 || true))"
until_ok 600 three_voters || fail "store lost a voter after the rollback"
until_ok 600 nodes_ready 3 || fail "nodes not Ready after the rollback"
v=$(ck 3 -n default get configmap e2e-marker -o jsonpath='{.data.n}')
log "node 3 rolled back: $(booted 3), etcd $(etcd_version 3), member $(member_id "$n3"), marker n=$v through its API server"
[ "$(etcd_version 3)" = "${ETCD_OLD:-3.7.2}" ] || fail "node 3 runs etcd $(etcd_version 3) after the rollback"
[ "$(member_id "$n3")" = "$id_before" ] || fail "node 3's store member changed: its data did not survive"
log "roll node 3 forward to $new_slot"
nk 3 patch bootentry "$new_slot" --type merge -p '{"spec":{"preferred":true}}' >/dev/null
reboot_node 3
sleep 30
until_ok 900 slot_good 3 "$NEW_VERSION" || fail "node 3 did not come back on the new slot ($(booted 3 || true))"
until_ok 600 three_voters || fail "store lost a voter after rolling forward"
[ "$(member_id "$n3")" = "$id_before" ] || fail "node 3's store member changed on rolling forward"
log "node 3 forward again: $(booted 3), etcd $(etcd_version 3)"
sleep 10
read -r rounds bad gap <<<"$(marker_report "$back_start")"
log "rollback and forward: $rounds marker rounds, $bad failed, longest gap ${gap}s"
[ "$gap" -le "${MAX_GAP:-30}" ] || fail "the cluster was unavailable for ${gap}s"
touch "$V/marker.stop"
wait "$MARKER_PID" || true

# --- backup and restore -----------------------------------------------------
age-keygen -o "$V/age.key" 2>/dev/null
recipient=$(age-keygen -y "$V/age.key")
ck 1 -n kube-system create secret generic kuberoot-state-backup \
  --from-literal=encryptionRecipient="$recipient" --from-literal=s3Endpoint=http://10.0.2.2:$S3_PORT \
  --from-literal=s3Bucket=backups --from-literal=s3AccessKey=e2e --from-literal=s3SecretKey=e2e-secret-key >/dev/null
marker_at_backup=$(ck 1 -n default get configmap e2e-marker -o jsonpath='{.data.n}')
nk 1 create -f - <<EOF >/dev/null
apiVersion: node.kuberoot.dev/v1alpha1
kind: StateBackup
metadata: {name: e2e}
EOF
nk 1 get statebackup e2e -o yaml >"$V/statebackup.yaml"
location=$(nk 1 get statebackup e2e -o jsonpath='{.status.location}')
sha=$(nk 1 get statebackup e2e -o jsonpath='{.status.sha256}')
[ -n "$location" ] || fail "the backup was not uploaded: $(nk 1 get statebackup e2e -o jsonpath='{.status.message}')"
log "backup e2e at marker n=$marker_at_backup: $location, encrypted $(nk 1 get statebackup e2e -o jsonpath='{.status.encrypted}')"
ck 1 -n default patch configmap e2e-marker --type merge -p '{"data":{"n":"after-backup"}}' >/dev/null
ck 1 -n default create configmap e2e-after-backup >/dev/null
object=$V/s3/${location#s3://}
[ -f "$object" ] || fail "no object at $object"
cp "$object" "$V/www/state.age"
[ "$(sha256sum "$V/www/state.age" | cut -d' ' -f1)" = "$sha" ] || fail "the uploaded archive does not match its sha256"

log "lose the cluster; install node 1 on a blank disk from the new image with the backup"
for i in 1 2 3; do down "$i"; done
mv "$V/n1" "$V/n1.lost"
media 1 "$NEW" kuberoot.datastore=etcd
up 1 "$V/media-1.img"
until_ok 600 nk 1 get osconfigs || fail "node 1: no node API on the live system"
nk 1 create -f - <<EOF >/dev/null
apiVersion: node.kuberoot.dev/v1alpha1
kind: Installation
metadata: {name: restore}
spec:
  disk: vdb
  reboot: true
  restore:
    url: http://10.0.2.2:$HTTP_PORT/state.age
    sha256: $sha
    identity: $(grep AGE-SECRET-KEY "$V/age.key")
EOF
until_ok 900 rebooted 1 ||
  fail "restore did not finish: $(nk 1 get installation restore -o jsonpath='{.status.phase} {.status.message}' || true)"
down 1
up 1
until_ok 600 slot_good 1 "$NEW_VERSION" || fail "node 1: restored system did not boot ($(booted 1 || true))"
until_ok 600 ck 1 get --raw /readyz || fail "restored node 1: cluster API not ready"
restored=$(ck 1 -n default get configmap e2e-marker -o jsonpath='{.data.n}')
after=$(ck 1 -n default get configmap e2e-after-backup -o name 2>/dev/null || true)
members=$(nk 1 get etcdmembers -o jsonpath='{range .items[*]}{.metadata.name}{" "}{end}')
log "restored node 1: $(booted 1), etcd $(etcd_version 1), members: $members, marker n=$restored, after-backup object: ${after:-none}"
[ "$restored" = "$marker_at_backup" ] || fail "marker is $restored, the backup had $marker_at_backup"
[ -z "$after" ] || fail "an object made after the backup is in the restored store"
log "PASS"
