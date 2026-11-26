#!/bin/bash
# Upgrade comparison uses the previous official minor-patch release as A,
# since baseline already uses the current pinned stable B. No datastore downgrade.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
require_stand
D=$FIELD_ROOT/k3s-upgrade-a
mkdir "$D"
export K3S_VERSION=v1.37.1+k3s1 K3S_DIR=$D DUT_KNOWN_HOSTS=$D/known_hosts
curl -fL --retry 3 -o "$D/sha256sum-amd64.txt" "https://github.com/k3s-io/k3s/releases/download/$K3S_VERSION/sha256sum-amd64.txt"
export K3S_SHA256=$(awk '$2=="k3s" {print $1}' "$D/sha256sum-amd64.txt")
[[ $K3S_SHA256 == 92b94582eb7b34a8cf532e6006842b9ed215a6783cf56a3dd6271fd35243b2c0 ]]
bash "$FIELD_SCRIPTS/prepare-k3s.sh"
export VM_DIR=$D KUBECONFIG=$D/kubeconfig SEED_MEDIA=$D/seed.iso
export PYTHON_IMAGE=$(python3 -c 'import json;print(json.load(open("/work/field-edge/downloads/workload-image.json"))["image"])')
bash "$FIELD_SCRIPTS/deploy-workload.sh"
bash "$FIELD_SCRIPTS/prepare-observer.sh"
# Install only the toolchain required by the fault injection, before measurements.
"$FIELD_SCRIPTS/dut-command.sh" 'sudo apt-get update && sudo apt-get install -y nftables; k3s --version; uname -r; dpkg-query -W "linux-image*"; systemctl is-enabled unattended-upgrades' > "$D/upgrade-baseline-public.txt"
kubectl -n field get pods -o json > "$D/workload-inventory.json"
bash "$FIELD_SCRIPTS/stop-dut.sh" "$D"
# Current stable B is copied from its verified official release, not built locally.
curl -fL --retry 3 -o "$D/k3s-target" 'https://github.com/k3s-io/k3s/releases/download/v1.37.2%2Bk3s1/k3s'
printf '265b3ee6a3198ef14033c63ef46cbb32370d83795199fb6d1b01f21575eddbda  %s\n' "$D/k3s-target" | sha256sum -c -
chmod 755 "$D/k3s-target"
"$D/k3s-target" --version > "$D/target-version.txt"
# Shared guarded fixture, compiled inside the pod only.
GOMAXPROCS=2 GOMEMLIMIT=1GiB CGO_ENABLED=0 "$FIELD_ROOT/build-main/go/bin/go" build -trimpath -ldflags='-s -w' -o "$D/fixture-agent" "$FIELD_SCRIPTS/fixture-agent.go"
