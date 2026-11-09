# Cloud stage0 acceptance

This workflow targets the new, installed amd64 cloud guest from `cloud-node.yaml`. Completion requires the recorded runtime evidence below. Use its cluster admin kubeconfig, not the outer workshop context. The manifest creates one ordinary Pod and one native `vm.kuberoot.dev` VirtualMachine; KubeVirt is only the outer laboratory hardware. One native disk replica tests persistence on this node, not failover. Budget-enabled live migration is disabled until the incoming memory size can be verified.

Run commands from the repository root on the builder or another operator host that can reach the new guest. Set the exact guest kubeconfig path obtained during installation; no helper defaults to the workshop cluster:

```sh
export CLOUD_SMOKE_KUBECONFIG=/work/kube/cloud-stage0-1.conf
export CLOUD_SMOKE_CONTEXT=$(kubectl --kubeconfig "$CLOUD_SMOKE_KUBECONFIG" config current-context)
test -n "$CLOUD_SMOKE_CONTEXT"
kc() { kubectl --kubeconfig "$CLOUD_SMOKE_KUBECONFIG" --context "$CLOUD_SMOKE_CONTEXT" "$@"; }
mkdir -p /work/cloud-stage0-smoke/before /work/cloud-stage0-smoke/after
kc cluster-info
kc get nodes -o wide
kc get osconfigs.node.kuberoot.dev -o yaml
kc get bootentries.node.kuberoot.dev -o yaml
```

Require `status.distro: kuberoot`, an amd64 node, and a booted installed root slot. OSConfig reads the distribution name from `/etc/os-release`; it does not identify the cloud profile. Establish that profile separately from the installed platform values, enabled services and kubelet reservation settings below. The installation owner must first switch the outer machine to its system disk and confirm persistent `/var`; a successful live-media boot is insufficient. A nonempty BootEntry with `status.booted: true` is useful API evidence. Record the actual node name, which is generated from its NIC and need not equal the outer KubeVirt VM name:

```sh
export CLOUD_SMOKE_NODE=$(kc get nodes -o jsonpath='{.items[0].metadata.name}')
test -n "$CLOUD_SMOKE_NODE"
test "$(kc get nodes -o jsonpath='{.items[*].metadata.name}' | wc -w | tr -d ' ')" = 1
kc wait --for=condition=Ready "node/$CLOUD_SMOKE_NODE" --timeout=10m
kc get repositories.kubepkg.dev,packages.kubepkg.dev
kc wait --for=condition=Ready packages.kubepkg.dev/kuberoot-cloud --timeout=15m
kc wait --for=condition=Ready packages.kubepkg.dev/kuberoot-edge --timeout=15m
kc wait --for=condition=Ready packages.kubepkg.dev/coredns --timeout=15m
kc wait --for=condition=Ready packages.kubepkg.dev/metrics-server --timeout=15m
kc wait --for=condition=Ready packages.kubepkg.dev/local-path-default --timeout=15m
kc get pods -A -o wide
kc get nodeservices.node.kuberoot.dev -o wide
kc get etcdmembers.node.kuberoot.dev -o yaml
kc get --raw "/api/v1/nodes/$CLOUD_SMOKE_NODE/proxy/configz"
kc get node "$CLOUD_SMOKE_NODE" -o jsonpath='{.status.capacity.memory}{" capacity; "}{.status.allocatable.memory}{" allocatable\n"}'
```

The running kubelet configuration must contain `systemReserved.memory: 6Gi` and `enforceNodeAllocatable: [pods]`. Confirm the `kuberoot-node`, `kuberoot-vmctl`, `kubepkg-operator`, containerd, kubelet and etcd services run. The boot must have selected `kuberoot.datastore=etcd`; the stage0 node must report one etcd member, with `learner: false` and `leader: true`, and no running kine service. Record member IDs and URLs before and after reboot. One member is the deliberate stage0 topology, not the later three-member HA acceptance.

Read each Package's Ready condition and observed generation if a wait fails; an absent Package is not success. Keep the upstream `main` Repository for existing packages and add a separate signed `cloud-stage0` catalog for the new `kuberoot-cloud` package. The resolver uses the union of configured repositories; the small stage0 catalog need not copy the entire upstream graph. Configure its actual index URL, signing public key and any plain-HTTP OCI registry addresses with the runtime owner, and record both Repository objects. Do not replace upstream `main` with an incomplete test catalog or use `allowUnsigned` to bypass a missing signature.

## Image preflight

The manifest uses the official [Debian Cloud Image Finder record](https://cloud-image-finder.debian.net/i/debian-13-genericcloud-amd64-20261001-2618), whose package inventory includes cloud-init, Python 3 and EFI GRUB. Its pinned qcow2 download link and SHA-512 digest are recorded below. HTTP availability and the qcow2 virtual size must be checked from the builder before applying the manifest; local DNS restrictions prevented a direct HEAD check during preparation. This is an amd64 image, not an arm64/Raspberry Pi validation.

```sh
export CLOUD_SMOKE_IMAGE=https://cloud.debian.org/cdimage/cloud/trixie/20261001-2618/debian-13-genericcloud-amd64-20261001-2618.qcow2
curl --fail --location --head "$CLOUD_SMOKE_IMAGE"
curl --fail --location "$CLOUD_SMOKE_IMAGE" --output /work/cloud-stage0-smoke/debian.qcow2
test "$(openssl dgst -sha512 -binary /work/cloud-stage0-smoke/debian.qcow2 | openssl base64 -A)" = '9G8GcablvexSkauJcrri8Q5UCML2SnQHjxHvwvBqQ2qdAxPtUOBHJULuq/eA6ffHkqwKMUxsIFB/zZ/YG0aMPQ=='
qemu-img info --output=json /work/cloud-stage0-smoke/debian.qcow2
```

Require virtual size no greater than 4 GiB. The runtime downloads the qcow2 and converts it onto a DRBD block device; it does not boot the compressed file directly. The 4 GiB backing disk plus the download and the runtime's 1 GiB free-space reserve must fit the installed state filesystem. If the pinned source cannot be fetched or verified, resolve that first and update the exact URL/digest together; do not replace the URL with an unrecorded `latest` image.

## Simultaneous Pod and native VM

Apply once for a fresh run. If the namespace, ordinary Pod and VM already exist from the current run, continue with observation; do not recreate them. An earlier optional `/dev/kvm` hostPath diagnostic was rejected by the installed cluster's baseline Pod Security policy. Record that diagnostic as rejected, not passed. It has been removed; keep the namespace policy unchanged and prove functional KVM using native guest execution below.

```sh
kc apply -f hack/hetzner/cloud-smoke.yaml
kc wait -n cloud-smoke --for=condition=Ready pod/ordinary-pod --timeout=5m
kc wait --for=jsonpath='{.status.phase}'=Running virtualmachines.vm.kuberoot.dev/cloud-smoke-vm --timeout=15m
kc get virtualmachines.vm.kuberoot.dev/cloud-smoke-vm -o yaml
kc get machines.node.kuberoot.dev,volumes.node.kuberoot.dev -o wide
export CLOUD_SMOKE_MACHINE_PID=$(kc get machines.node.kuberoot.dev "$CLOUD_SMOKE_NODE.cloud-smoke-vm" -o jsonpath='{.status.pid}')
test "$CLOUD_SMOKE_MACHINE_PID" -gt 0
kc get machines.node.kuberoot.dev "$CLOUD_SMOKE_NODE.cloud-smoke-vm" -o json > /work/cloud-stage0-smoke/before/native-machine.json
export CLOUD_SMOKE_ADDRESS=$(kc get virtualmachines.vm.kuberoot.dev/cloud-smoke-vm -o jsonpath='{.status.address}')
test -n "$CLOUD_SMOKE_ADDRESS"
kc get --raw "/apis/node.kuberoot.dev/v1alpha1/machines/$CLOUD_SMOKE_NODE.cloud-smoke-vm/console"
kc exec -n cloud-smoke ordinary-pod -- nslookup kubernetes.default.svc.cluster.local
kc exec -n cloud-smoke ordinary-pod -- wget -T 10 -qO- "http://$CLOUD_SMOKE_ADDRESS:8080/identity"
kc exec -n cloud-smoke ordinary-pod -- wget -T 10 -qO- "http://$CLOUD_SMOKE_ADDRESS:8080/boot-id"
```

`Running` and a positive native Machine PID mean the monitor is alive; they do not prove the guest OS booted. Require those API records, the serial `KUBEROOT_CLOUD_SMOKE_READY` marker and successful HTTP responses before passing. Both requests run inside the ordinary Pod, proving simultaneous Pod operation, guest boot, DHCP, routing and cloud-init. Successful execution in the native cloud-hypervisor guest establishes functional nested KVM on this amd64 Linux node: this backend has no software CPU emulation fallback. This is stronger evidence than merely finding a character device. The guest's HTTP server uses packages already present in the image and installs nothing on first boot. Preserve the PID/console/HTTP evidence again after reboot.

## Observed memory, including native services

Collect actual usage with both workloads running after the metrics packages become Ready. Capacity and allocatable alone are configuration evidence, not measurements:

```sh
export CLOUD_SMOKE_EVIDENCE=/work/cloud-stage0-smoke/before
kc top nodes > "$CLOUD_SMOKE_EVIDENCE/top-nodes.txt"
kc top pods -A > "$CLOUD_SMOKE_EVIDENCE/top-pods.txt"
cat "$CLOUD_SMOKE_EVIDENCE/top-nodes.txt" "$CLOUD_SMOKE_EVIDENCE/top-pods.txt"
kc get osconfigs.node.kuberoot.dev "$CLOUD_SMOKE_NODE" -o json > "$CLOUD_SMOKE_EVIDENCE/os-memory.json"
kc get --raw "/apis/metrics.k8s.io/v1beta1/nodes/$CLOUD_SMOKE_NODE" > "$CLOUD_SMOKE_EVIDENCE/node-metrics.json"
kc get nodeservices.node.kuberoot.dev -o custom-columns='NAME:.metadata.name,STATE:.status.state,PID:.status.pid,MEMORY_BYTES:.status.memoryBytes'
kc get nodeservices.node.kuberoot.dev -o json > "$CLOUD_SMOKE_EVIDENCE/native-services.json"
python3 - <<'PY'
import json
import os
import re
from pathlib import Path
root = Path(os.environ['CLOUD_SMOKE_EVIDENCE'])
os_status = json.loads((root / 'os-memory.json').read_text())['status']
usage = json.loads((root / 'node-metrics.json').read_text())['usage']['memory']
match = re.fullmatch(r'(\d+)(Ki|Mi|Gi|Ti)?', usage)
if not match:
    raise SystemExit('Record and parse the unexpected metrics quantity: ' + usage)
units = {None: 1, 'Ki': 2**10, 'Mi': 2**20, 'Gi': 2**30, 'Ti': 2**40}
working = int(match[1]) * units[match[2]]
total, free = os_status['memoryTotal'], os_status['memoryFree']
print({'nodeWorkingSetBytes': working, 'memoryTotalBytes': total,
       'memoryFreeBytes': free, 'totalMinusFreeBytes': total - free,
       'workingSetPercentOfTotal': round(100 * working / total, 2)})
PY
```

OSConfig `memoryFree` is Linux free RAM, not MemAvailable; `totalMinusFreeBytes` includes reclaimable cache. Node metrics supply a different working-set measure. Calculate the HA usage percentage against total RAM yourself: the percentage printed by `kubectl top nodes` may use allocatable, from which the VM pool was intentionally removed. Record both measurements and explain which one is compared with the approximately 60% usage target. `NodeService.status.memoryBytes` reports service-cgroup usage. Native cloud-hypervisor children inherit the kuberoot-node service cgroup, so that service's figure includes API/monitor/guest allocation together; the API does not currently expose per-monitor RSS. Do not present these aggregates as guest-only RAM. Repeat these commands after reboot with `CLOUD_SMOKE_EVIDENCE=/work/cloud-stage0-smoke/after` so the original measurements remain intact.

## Direct node API budget rejection

With the 768 MiB VM running, this second 4096 MiB request would exceed the node's 4096 MiB pool. It goes directly to `machines.node.kuberoot.dev`, bypassing VM-controller placement. Its deliberately nonexistent volume prevents a guest from booting even if admission is broken. Run the test only on this isolated smoke node:

```sh
cat > /work/cloud-stage0-smoke/over-budget.yaml <<EOF
apiVersion: node.kuberoot.dev/v1alpha1
kind: Machine
metadata:
  name: $CLOUD_SMOKE_NODE.cloud-smoke-over-budget
spec:
  cpus: 1
  memoryMiB: 4096
  running: true
  volumes: [cloud-smoke-no-such-volume]
EOF
if kc create -f /work/cloud-stage0-smoke/over-budget.yaml > /work/cloud-stage0-smoke/budget-result.txt 2>&1; then
  kc delete machines.node.kuberoot.dev "$CLOUD_SMOKE_NODE.cloud-smoke-over-budget"
  printf 'FAIL: direct node API accepted an over-budget machine\n'
  exit 1
fi
cat /work/cloud-stage0-smoke/budget-result.txt
grep -F 'machine memory budget exceeded' /work/cloud-stage0-smoke/budget-result.txt
kc get machines.node.kuberoot.dev "$CLOUD_SMOKE_NODE.cloud-smoke-over-budget" --ignore-not-found
```

The create must fail with Forbidden and the budget message, and the final get must be empty. A timeout, authentication error, missing API or volume error does not pass. Repeat after reboot to exercise restored reservations. Concurrent admission, live shrink/stop, malformed persisted specs and interrupted launch recovery have separate unit regressions; this smoke alone does not prove those races.

## Installed-system reboot persistence

Save evidence before the reboot. Keep the output directory on the builder, not inside the guest:

```sh
mkdir -p /work/cloud-stage0-smoke/before /work/cloud-stage0-smoke/after
kc get namespace cloud-smoke -o jsonpath='{.metadata.uid}' > /work/cloud-stage0-smoke/before/namespace-uid
kc get pod -n cloud-smoke ordinary-pod -o jsonpath='{.metadata.uid}' > /work/cloud-stage0-smoke/before/pod-uid
kc get virtualmachines.vm.kuberoot.dev/cloud-smoke-vm -o jsonpath='{.metadata.uid}' > /work/cloud-stage0-smoke/before/vm-uid
kc get virtualmachines.vm.kuberoot.dev/cloud-smoke-vm -o jsonpath='{.status.address}' > /work/cloud-stage0-smoke/before/address
kc get osconfigs.node.kuberoot.dev "$CLOUD_SMOKE_NODE" -o jsonpath='{.status.bootTime}' > /work/cloud-stage0-smoke/before/node-boot-time
kc exec -n cloud-smoke ordinary-pod -- wget -T 10 -qO- "http://$CLOUD_SMOKE_ADDRESS:8080/identity" > /work/cloud-stage0-smoke/before/guest-identity
kc exec -n cloud-smoke ordinary-pod -- wget -T 10 -qO- "http://$CLOUD_SMOKE_ADDRESS:8080/boot-id" > /work/cloud-stage0-smoke/before/guest-boot-id
kc get packages.kubepkg.dev,virtualmachines.vm.kuberoot.dev,machines.node.kuberoot.dev,volumes.node.kuberoot.dev,bootentries.node.kuberoot.dev,etcdmembers.node.kuberoot.dev -o yaml > /work/cloud-stage0-smoke/before/resources.yaml
```

Reboot this installed guest through its OSConfig. Do not reinstall it or reapply the smoke manifest between observations:

```sh
export CLOUD_SMOKE_REBOOT_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
kc patch osconfigs.node.kuberoot.dev "$CLOUD_SMOKE_NODE" --type=merge -p "{\"spec\":{\"rebootRequestedAt\":\"$CLOUD_SMOKE_REBOOT_AT\"}}"
```

Observe the API outage and wait for `status.bootTime` to change; a Ready result from just before shutdown does not prove a reboot. Then repeat the readiness and simultaneous Pod/VM checks above, without recreating resources. Collect the same fields into `/work/cloud-stage0-smoke/after/`. Compare namespace, Pod and VM UIDs, VM address and guest identity: all must stay identical. Node boot time and the native guest's boot ID must change. The VM/volume must still report `imageWritten: true`, the installed root slot must remain booted, the Packages must be Ready again, and the direct budget rejection must still work.

Record serial output, API objects, package conditions and all comparison results with the tested source revision and image digest. This establishes one-node installed-system persistence. It does not establish arm64 CPU hotplug, physical Raspberry Pi KVM, live migration, DRBD replication, or three-node failure recovery. Leave test resources in place until evidence has been reviewed; cleanup deletes the test VM's disk.
