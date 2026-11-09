# Cloud stage1 acceptance

Use a separate installed amd64 cloud lab from `cloud-resize-node.yaml`. Keep the accepted stage0 VM, disks, immutable source and artifacts untouched. Build the stage1 artifacts under `/work/out/stage1`, set `KUBEROOT_VERSION=0.1.0-stage1.1`, retain the same kernel and DRBD modules, and install onto the new VM's confirmed empty system disk. Detach its media before acceptance. This checks amd64 integration, not physical Pi5 or ARM CPU hotplug.

Set `CLOUD_RESIZE_KUBECONFIG` to the new inner cluster's kubeconfig and use its explicit context on every command. The host must report the stage1 version, a Good booted installed slot, Node Ready, both signed package repositories and Ready cloud/edge packages. Verify `systemReserved.memory=6Gi` and the 4096MiB native VM pool. This test reserves that entire pool with one 4GiB ceiling; no other native VM should share the stage1 node.

```sh
export CLOUD_RESIZE_KUBECONFIG=/work/kube/cloud-stage1-1.conf
export CLOUD_RESIZE_EVIDENCE=/work/stage1/acceptance
mkdir -p "$CLOUD_RESIZE_EVIDENCE"
kc() { /work/bin/kubectl --kubeconfig "$CLOUD_RESIZE_KUBECONFIG" --context kuberoot --request-timeout=20s "$@"; }
kc get nodes,bootentries.node.kuberoot.dev,packages.kubepkg.dev
kc apply -f hack/hetzner/cloud-resize.yaml
kc wait -n cloud-resize --for=condition=Ready pod/observer --timeout=5m
kc wait --for=jsonpath='{.status.phase}'=Running virtualmachine.vm.kuberoot.dev/cloud-resize-vm --timeout=10m
export CLOUD_RESIZE_ADDRESS=$(kc get virtualmachine.vm.kuberoot.dev/cloud-resize-vm -o jsonpath='{.status.address}')
export CLOUD_RESIZE_NODE=$(kc get virtualmachine.vm.kuberoot.dev/cloud-resize-vm -o jsonpath='{.status.node}')
kc exec -n cloud-resize observer -- wget -T 10 -qO- "http://$CLOUD_RESIZE_ADDRESS:8080/" > "$CLOUD_RESIZE_EVIDENCE/before.json"
kc get machine.node.kuberoot.dev "$CLOUD_RESIZE_NODE.cloud-resize-vm" -o json > "$CLOUD_RESIZE_EVIDENCE/machine-before.json"
kc get virtualmachine.vm.kuberoot.dev/cloud-resize-vm -o json > "$CLOUD_RESIZE_EVIDENCE/vm-before.json"
```

Wait for the HTTP endpoint to answer before changing the size. Its live JSON includes `nproc`, the full `/proc/meminfo`, `/proc/uptime`, the boot ID and the online CPU list. The guest enables virtio-mem and CPU onlining with a udev rule; failure to load the module fails the HTTP service and must be investigated rather than treated as a successful hotplug. The pinned Debian image and digest are the same as `cloud-smoke.md`.

Start watches before requesting growth, save their complete output and stop only those watch processes after collecting the final state. Then patch `cpus: 2` and `memory: 2Gi` together. Retain the original ceilings, VM UID, disk and address. Allow guest onlining to finish before evaluating its counters; a successful VMM API call alone is insufficient.

```sh
kc get virtualmachine.vm.kuberoot.dev/cloud-resize-vm --watch --output-watch-events -o json > "$CLOUD_RESIZE_EVIDENCE/vm-watch.json" &
vm_watch=$!
kc get machine.node.kuberoot.dev "$CLOUD_RESIZE_NODE.cloud-resize-vm" --watch --output-watch-events -o json > "$CLOUD_RESIZE_EVIDENCE/machine-watch.json" &
machine_watch=$!
kc patch virtualmachine.vm.kuberoot.dev/cloud-resize-vm --type=merge -p '{"spec":{"cpus":2,"memory":"2Gi"}}'
# Poll the HTTP endpoint and node Machine until online resources converge.
kc exec -n cloud-resize observer -- wget -T 10 -qO- "http://$CLOUD_RESIZE_ADDRESS:8080/" > "$CLOUD_RESIZE_EVIDENCE/after.json"
kc get machine.node.kuberoot.dev "$CLOUD_RESIZE_NODE.cloud-resize-vm" -o json > "$CLOUD_RESIZE_EVIDENCE/machine-after.json"
kc get virtualmachine.vm.kuberoot.dev/cloud-resize-vm -o json > "$CLOUD_RESIZE_EVIDENCE/vm-after.json"
kill "$vm_watch" "$machine_watch"
wait "$vm_watch" "$machine_watch" || true
```

Require guest `nproc` to change from 1 to 2, MemTotal to grow from approximately 1GiB to approximately 2GiB, boot ID unchanged, and uptime increased. The native monitor PID, VM UID and address must remain unchanged. Neither watch may report Starting, Stopped, Failed or a different monitor PID during the operation. The node Machine's applied `status.cpus=2`, `status.memoryMiB=2048` and `ResourcesReady=True` must agree with the guest. VirtualMachine `phase=Running` alone does not confirm a resize; applied sizes and the condition belong to the node Machine API.

Negative acceptance includes both APIs. On the cluster VM, changing either ceiling, adding one to a legacy VM, removing a ceiling, requesting CPUs above the ceiling or bounded RAM outside 128Mi steps must fail admission. On direct node Machine create/update, perform equivalent ceiling checks. While the 1GiB guest reserves its 4GiB ceiling, a second running Machine asking for only 128MiB must fail Forbidden with the memory-budget message and leave no persisted object. Use a nonexistent volume in rejected candidates so a broken admission check cannot accidentally boot them. A stopped guest is not a substitute for this test.

After proving growth without reboot, request a shrink to 1 CPU/1GiB on this test VM. Require a new guest boot ID and monitor PID, the same VM UID/address/disk, and guest counters back near the initial values. The VM ceiling stays 4GiB and a second 128MiB request must still be denied. Preserve serial logs and all before/after objects; do not delete the lab's VM or volume as part of acceptance.

Incoming migration is rejected when a memory pool or explicit ceilings are configured. Sending a bounded VM is also rejected until the asynchronous completion and receiver allocation contracts are implemented. These refusals are documented limitations, not successful live-migration tests. Record source and artifact hashes, generator output, full and race test results, actual guest observations and the limits of this amd64 result in the stage1 report.
