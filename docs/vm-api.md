# Namespaced virtual machines

`vm.kuberoot.dev/v1alpha2` stores VirtualMachines in namespaces. Two namespaces can each contain `web` without sharing a node Machine, Volume, DHCP reservation or cloud-init identity. The controller binds a new VM to `vm-<API UID>` in `status.backendID` and uses its UID as `status.instanceID`. Both fields are controller-owned, globally unique and immutable once set. Tenant RBAC must exclude `virtualmachines/status`; editing ordinary VM spec cannot set these bindings.

`spec.userDataSecret` contains only `name`. The seed handler reads that Secret from the VM's namespace and verifies the requesting guest's source address. Workspace VMs and their Secrets remain in the `workstation` namespace, with the existing `ws-<workspace>` name. Workspace remains cluster-scoped in this step. CPU and memory ceilings retain the [resource contract](cloud.md#guest-resource-ceilings).

`spec.seedMode` is an optional creation-time choice: `NoCloud` or `NoCloudNet`. Omitted or explicit `NoCloud` retains the exact existing `ds=nocloud` SMBIOS serial and does not expose `network-config`. New `NoCloudNet` machines use `ds=nocloud-net` and receive network-config v2 with DHCPv4 matched to their bound NIC's MAC, independent of the guest interface name. The field cannot be added, removed or changed after creation; existing guests keep their original launch fingerprint. New CAPI machines opt into `NoCloudNet`. This follows [Talos v1.13.6's network-source selection and MAC-matched network configuration](https://github.com/siderolabs/talos/blob/v1.13.6/internal/app/machined/pkg/runtime/v1alpha1/platform/nocloud/metadata.go): HTTP seed requires `nocloud-net`, and a network configuration is needed for its metadata/hostname processing. The seed still requires the requesting guest's source address and a unique backend binding. No provider identity is fabricated in metadata.

## Existing cluster-scoped installations

Kubernetes cannot change a CRD's scope in place. The migration helper explicitly exports a private immutable backup, pauses every controller, detaches dependents, replaces only the cluster VM API objects and CRD, imports them into chosen namespaces and restores references using a saved old-to-new UID map. Native node Machines and Volumes are never deleted or rewritten by the helper. Imported VMs retain the old backend name, MAC, IP, disk placement and guest instance ID despite receiving a new API UID.

This is a maintenance operation, not an automatic startup action. Prepare, verify, cutover and resume are separate commands. A failure retains the controller gate and admission fences. The plan contains Secret data; it is written with mode 0600, fsync and atomic rename, and must not be pasted into logs. Keep the immutable plan and its SHA256 together with the separate durable state file. A partially completed cutover must use the same plan and checkpoint; do not recreate a new backup from partial state.

Every node must first run the transition node API with checked storage inventory and controller process inventory. Every vmctl/wsctl process, including standby replicas, must run the maintenance guard. A missing old API field, unreadable node, orphan process, different executable, PID/start incarnation mismatch or stale acknowledgement rejects cutover. The gate drains reconcile, sweep and seed HTTP handlers; new seed requests receive 503 until the gate is released. Unknown processes cannot be inferred absent from a missing supervised service.

A transition image is built explicitly with `KUBEROOT_VM_API_TRANSITION=1` for cloud, hypervisor or workstation. It carries the immutable `/usr/share/kuberoot/vm-api-transition` marker; ordinary images omit it and refuse the legacy controller branch. The installed controller executable contains two isolated implementations. At process startup it selects the frozen legacy controller only for a CRD that stores and serves cluster-scoped v1alpha1; it selects the normal controller only for namespaced v1alpha2. Both implementations check their scope again before every reconcile pass. The legacy packages under `internal/legacyvmctl` and `internal/legacywsctl` record their exact accepted stage1 provenance and contain no alpha2 binding adaptation. The shared guard reports its selected API mode and authenticated username in each acknowledgement. An unknown mode or principal is not a valid barrier participant.

Roll out this transition-capable executable and node API to every node before taking the guest-preservation baseline. A host image upgrade may reboot the host and guests; that rollout is outside the later process-only API cutover proof. While the actual CRD is still legacy, the guarded legacy controller retains its existing backend, network and Secret behavior. The transition image keeps its alpha2 VM CRD outside the ordinary add-on batch. An independent reader applies it only when there is no maintenance gate and the installed VM CRD is either absent or already namespaced alpha2. A legacy scope, unknown response or unreadable gate defers only this CRD; other add-ons, node role labelling and package installation can finish. The gate is checked again after observing scope, so a NotFound window during explicit replacement does not make the image create the CRD ahead of the importer. It never bundles or reapplies an alpha1 CRD. After a completed cutover or a subsequent reboot, the same transition image applies its normal alpha2 CRD. Ordinary images retain the regular alpha2 add-on behavior. After the helper has imported and verified alpha2, use `NodeService.spec.restartedAt` to restart only every vmctl/wsctl process, including standby replicas. The same installed binary now selects alpha2 without restarting kinit, the node API or a guest monitor. Resume refuses to release the gate until all current process incarnations acknowledge alpha2. The controller restart can briefly interrupt DHCP/gateway, seed HTTP and workstation sessions; preservation of guest execution is a separate measured property. Validate this complete sequence on a disposable installed lab first.

The helper inventories all node Machines and Volumes node by node, including backends omitted from stale VM status after a crash. Missing or uncheckpointed replicas, a Running VM without a live monitor PID/start time, active migration or a volume still allowing two primaries must be resolved before export. It discovers dependents across the API and repeats that discovery immediately before deleting old VM objects. A new or unreadable dependent stops deletion. Native Volume UIDs are name-derived; preserving an API binding alone does not prove durable disk identity. Capture the guest filesystem UUID, persistent marker/hash, boot ID, uptime and monitor incarnation before and after the actual cutover.

## Direct admission proof

Supply a JSON map from every current control-plane node to its direct `https://InternalIP:port` API listener. Each endpoint must be a unique current Node InternalIP, with verified TLS and no proxy or load balancer. The operator must confirm these are the local API listeners; an IP/port check cannot detect an operator-supplied forwarding listener. An ordinary load-balanced cluster endpoint cannot prove propagation to every API server.

The helper creates a random session user with no credentials and a namespace Role that only permits creating ConfigMaps in `kube-system`. The operator must be authorized to impersonate that user. The helper sends explicit dry-run probes directly to every control plane and accepts only a Forbidden denial identifying its exact policy, session token and current protected-scope hash. A generic RBAC refusal, another policy's denial, an old policy version or a successful dry-run is insufficient. The production role does not grant access to VM spec/status or Secrets. Probe RBAC and the admission fence are removed with token checks and UID preconditions after the explicit controller release boundary. A crash during that cleanup resumes under the original executor identity without treating legal post-release controller changes as import corruption.

The policy fences VM and Workspace mutations, both source and target bootstrap Secrets, and objects whose old or new owner references name an exported VM UID. Only the recorded executor may modify protected objects during maintenance. The helper verifies exact imported VM state, Secret data/type, owner references and running backend identities before checkpointing verification and repeats that verification before resume, including recovery after a crash in the `resuming` phase. Successful gate deletion is the release boundary; subsequent ordinary controller updates are no longer compared with the frozen import snapshot.

## Operator sequence

Use a dedicated administrative identity with permission to export resources, manage the temporary policy/RBAC and impersonate the session probe user. Before creating a maintenance gate, the helper verifies each controller’s authenticated username against its current PID/start incarnation and rejects an executor that shares any controller principal. The guard obtains only the username from SelfSubjectReview; it does not expose groups, tokens, credentials or extra claims. Use the same executor kubeconfig for every prepare/cutover/resume invocation, including cleanup after release. This check does not discover every external writer: the dedicated identity must not be shared with other automation or interactive sessions. Create the destination namespaces first. Map every existing VM exactly once; a Workspace VM must map to `workstation`. A VM-owned dependent must already be in that destination namespace. Resolve target Secret collisions before preparing; the helper refuses to overwrite an unrelated Secret.

```sh
# Names and endpoints below are examples for a disposable lab.
cat > namespace-map.json <<'JSON'
{"web":"tenant-a","ws-anna":"workstation"}
JSON
cat > api-servers.json <<'JSON'
{"cp-a":"https://192.0.2.10:6443","cp-b":"https://192.0.2.11:6443","cp-c":"https://192.0.2.12:6443"}
JSON
vm_api_migrate() {
  /work/bin/kuberoot-vm-api-migrate \
    --kubeconfig /work/kube/disposable-stage2.conf --context kuberoot \
    --api-server-endpoints api-servers.json --plan vm-api-plan.json "$@"
}
vm_api_migrate --mode prepare --namespace-map namespace-map.json \
  --target-crd distros/cloud/addons/vm.kuberoot.dev_virtualmachines.yaml
# Review the saved plan privately and record the printed SHA256.
vm_api_migrate --mode verify
vm_api_migrate --mode cutover --execute-cutover \
  --plan-sha256 "$VM_API_PLAN_SHA256" --state vm-api-state.json
vm_api_migrate --mode verify --plan-sha256 "$VM_API_PLAN_SHA256" --state vm-api-state.json
# Restart only the known vmctl/wsctl NodeServices; do not restart the host.
# Repeat for every actual participant, including standby controllers.
kc() { kubectl --kubeconfig /work/kube/disposable-stage2.conf --context kuberoot "$@"; }
VM_API_RESTART_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
kc patch nodeservice.node.kuberoot.dev cp-a.kuberoot-vmctl --type=merge \
  -p "{\"spec\":{\"restartedAt\":\"$VM_API_RESTART_AT\"}}"
# Wait for fresh api-mode=v1alpha2 acknowledgements from all participants.
# Independently compare running monitor/guest/disk evidence before release.
vm_api_migrate --mode resume --resume-controllers \
  --plan-sha256 "$VM_API_PLAN_SHA256" --state vm-api-state.json
```

Do not remove the gate or admission policy manually after a failure. Retry the interrupted explicit mode with the same private plan, hash and checkpoint after resolving its reported precondition. A plan cannot silently adopt an existing target VM, change another import's backend binding or replace a target with a different UID. The prepare command reuses an existing plan only after verifying its unchanged source inventory.

The current source has unit/crash recovery tests and opt-in admission integration against two isolated API listeners. That API test is not a running-guest cutover acceptance. Installed runtime acceptance must additionally show no monitor or guest restart, unchanged disk markers/UUIDs and network identity, correct new namespaces and bindings, namespace/Secret isolation, and successful controller reconciliation after release. Preserve accepted stage0/stage1 labs and run this proof in a separate stage2 lab. A CAPI infrastructure provider is a subsequent layer over the namespaced API.
