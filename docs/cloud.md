# Cloud distribution

`cloud` combines edge's pods, scheduler and kubepkg operator with native cloud-hypervisor virtual machines and DRBD disks. The VM runtime remains a process of the node. The `kuberoot-cloud` recipe in kubepkg-recipes depends on `kuberoot-edge` for DNS, metrics and default local storage. The combined distribution is under development; the acceptance work described below is not yet complete.

Build with `KUBEROOT_DISTRO=cloud hack/hetzner.sh build amd64` (or `arm64`). Start the first node with `kuberoot.datastore=etcd`; the datastore choice cannot be changed on an existing kine cluster. Install three control planes on NVMe for the three-member etcd topology. The VM controller uses a Lease so only one control plane owns placement and the machines' network gateway.

ARM VM distributions build a custom Cloud Hypervisor through `scripts/build-vmm.sh arm64`. The Linux builder needs Rust 1.93.0 with the `aarch64-unknown-linux-gnu` target installed through rustup, GNU cross GCC 12, git, Python 3 and binutils. An isolated compiler wrapper can be selected with `CROSS_LINKER` when invoking that script directly. The recipe verifies the upstream archive, reviewed patch and Cargo.lock, builds both binaries with the release profile and static linking, and records their checksums and toolchain in `$KUBEROOT_OUT/arm64/vmm`. Image assembly requires matching artifacts and embeds `/usr/share/kuberoot/cloud-hypervisor-build.json`. There is no stock ARM fallback when the custom build is missing or stale. The amd64 build retains its existing upstream binaries.

Rebuild the kernel and signed DRBD modules with `KUBEROOT_DISTRO=cloud hack/hetzner.sh kernel arm64` when adopting the CPU/memory hotplug configuration; the ordinary build command reuses an existing kernel of the same flavour. The guest kernel and kinit CPU onlining helper must be updated together. Physical-board and complete-image validation remain required below.

## Memory ownership

The profile targets nodes with at least 8 GiB. It reserves 3.5 GiB from pod allocatable memory: 1 GiB for guest memory and 2.5 GiB for native services and VM process overhead, leaving about 4 GiB for pods. The node API's `--machine-memory-budget-mib=1024` enforces the guest pool. Pod cgroups enforce allocatable memory through `enforceNodeAllocatable: [pods]`.

Both earlier reservations were too small, and the way they failed is worth knowing. A reservation sized for the guests alone, 4 GiB of pool and 2 GiB of services, left 1.6 GiB of allocatable memory and the platform could not be installed at all. Raising it to 2 GiB of services made the installation succeed and then fail differently: the kubelet placed pods against memory the node's own processes were already using, the node evicted them, each eviction left an object behind, and the API server's cache grew on those objects until it was evicting thousands of pods an hour. Measure the services on a running node before choosing this number; on this profile they cost about 3.3 GiB.

Every native service carries a `GOMEMLIMIT` in its environment, sized at roughly twice its measured steady state. Go's collector otherwise lets the heap grow to twice the live set and does not return it, so a single burst -- a mass delete, a cache rebuild -- raises a service's memory permanently. The API server reached 2.1 GiB that way and stayed there until restarted, which on a board is the difference between a platform that fits and one that does not. When changing the profile for another machine size, change the VM pool, the kubelet reservation and these ceilings together, and check what is left against the platform's own footprint.

Admission checks direct node API creates and updates as well as starts. A stop or shrink does not free a still-running guest's allocation. Concurrent requests share the same budget. An interrupted launch leaves a reservation in `/run` so an API restart cannot forget a possibly live monitor; recovering that monitor or rebooting the host clears the ambiguity. A host reboot clears both runtime reservations and the old processes.

Live migration reception is temporarily refused when the memory budget is enabled. The migration stream can carry more guest RAM than the receiving Machine specification declares; trusting that specification would bypass admission. A verified allocation contract is required before enabling live migration in this profile. Stop any existing migration receiver before enabling the budget on a legacy node. Cold starts use normal admission, including recovery after a host reboot.

This reservation prevents the scheduler from assigning the VM pool to pod requests. It does not prove that a three-node cluster survives losing a node. Measure actual per-node usage with the intended databases, VMs and tenant clusters, including native processes and storage overhead, and keep it below approximately 60% so the two remaining nodes can absorb the lost node's work. A pod cannot exceed one physical board's memory. Local-path storage supplied by the base package is not replicated; platform workloads that must survive a node loss need replicated storage.

## VM controller takeover

With leader election enabled, VM address reservations are written to API status before DHCP offers them. `status.address` therefore reports the assigned address, not proof that the guest has obtained it. The next leader reconstructs the DHCP reservations from the API. A graceful handoff stops DHCP and the seed server and removes the old gateway address before releasing the Lease. The new leader announces its gateway MAC to the guests.

A process restart cleans its stale gateway state, and its DHCP subprocess is tied to the controller's lifetime. A Lease alone cannot fence a stalled or partitioned host's kernel networking. Abrupt controller failure and network partitions still need runtime verification and a fencing design before HA networking can be claimed. Migrating a legacy single-gateway installation requires stopping its controller and adopting local DHCP leases on the original gateway before enabling replicated controllers; a placed VM whose address cannot be recovered is refused rather than silently renumbered.

## Acceptance still required

The combined image needs a live run showing a Ready node, a running pod and VM at the same time, package installation, and restoration after reboot. Oversubscription must be refused at the node API, including concurrent requests, restored machine specifications and incoming migrations. The HA gateway must retain addresses and stop serving before another controller takes over. A test using nested KVM is a software integration test, not evidence about physical Raspberry Pi hardware.

Raspberry Pi 5 KVM with this image's boot path has not been verified on hardware. Its kernel must enter at EL2 and expose `/dev/kvm`; DRBD and NVMe also need physical validation. [Upstream cloud-hypervisor v53.0 requires GICv3 on AArch64](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/v53.0/README.md#prerequisites-for-aarch64), while the [BCM2712 device tree describes a GICv2 controller](https://github.com/torvalds/linux/blob/master/arch/arm64/boot/dts/broadcom/bcm2712.dtsi). The custom ARM port adds GICv2 support while retaining GICv3 preference on compatible hosts. A physical boot test and a complete distribution run are still required before claiming support for the board.

The custom port's CPU and memory hot-add path, including a paused snapshot/restore of the expanded guest, has been exercised in an ARM/GICv2 emulated host with a nested KVM guest. Complete-image hot-add through the node API and physical Pi5 behavior remain separate acceptance requirements. [Upstream v53.0 CPU hotplug](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/v53.0/docs/hotplug.md) alone does not provide this ARM capability.

Isolation between untrusted tenants remains unfinished: native guests currently share one bridge/VXLAN segment, and their seed endpoint authorizes requests using the assigned source IP. Namespaced objects provide API ownership and RBAC, while port anti-spoofing and tenant network policy still need implementation and validation. Current VM experiments must stay within one trust domain. The independent disk lifecycle needed by the tenant VM catalog also remains unfinished.

## Guest resource ceilings

A new VirtualMachine can opt into live growth with `maxCPUs` and `maxMemory`. Both ceilings are immutable, including adding or removing them later. The node API exposes the same contract as `maxCPUs` and `maxMemoryMiB`. A machine without ceilings keeps the original behavior: changing its size restarts it, and upgrading the node does not change its launch fingerprint or restart it merely to adopt the new API.

```yaml
apiVersion: vm.kuberoot.dev/v1alpha2
kind: VirtualMachine
metadata:
  name: worker
  namespace: default
spec:
  cpus: 1
  maxCPUs: 4
  memory: 1Gi
  maxMemory: 4Gi
  replicas: 1
  disk:
    size: 4Gi
    image: https://example.org/debian-13-uefi.qcow2
```

Increase `cpus` and `memory` within these ceilings to request live growth. Cloud Hypervisor's committed configuration is observed after each resize; a successful CPU change followed by a failed RAM change is retried from that observed state. The machine stays `Running`, while its node Machine's `ResourcesReady` condition and message expose an incomplete resize. Its status `cpus` and `memoryMiB` describe the VMM's applied requests, not guest online resources. The guest needs CPU and virtio-mem hotplug support and may online them asynchronously; verify its CPU count and `/proc/meminfo` separately. Shrinking either resource restarts the guest instead of waiting indefinitely for memory to be released.

An ARM launch with `maxCPUs` requires the installed VMM's versioned `--host-capabilities` response to confirm KVM/GICv2 hot-add, with a ceiling of eight vCPUs. The same opened executable is used for the probe and launch. Unsupported hosts or failed probes reject the launch before an existing guest is stopped. A running ARM guest must report `config.cpus.acpi_hotplug: true` in its own `vm.info` before CPU growth; an older guest reports `CPUHotplugUnavailable` and remains running until an explicit maintenance restart. Unchanged CPU counts and memory-only growth continue to work for older guests without this CPU mode. Installing a new VMM does not change the launch fingerprint or trigger a restart.

When `maxMemory` is set, both memory values must be positive multiples of 128Mi. Cloud Hypervisor v53 requires a 128Mi-aligned virtio-mem region; this explicit request step also permits a later shrink and restart without rounding or changing the ceiling. Legacy memory requests remain unrestricted by this step. The entire `maxMemory` is charged to the node's VM pool even while only a small part is plugged or touched. Ballooning, the guest's reported free RAM and `vm.info.memory_actual_size` never lower this reservation. A 4Gi ceiling therefore exceeds the cloud profile's default 1Gi pool on its own and is rejected.

Ceilings do not enable live migration: incoming migration remains rejected for a budget-enabled node or a machine with explicit ceilings until its allocation and completion contracts can be enforced. Sending a bounded VM is also rejected: the current asynchronous migration protocol cannot yet prove success before stopping the source. These refusals are temporary limitations, not migration acceptance. The current integration must be accepted on amd64 separately from physical Raspberry Pi and ARM CPU hotplug; those hardware capabilities are not inferred from x86 results. The largest guest or pod remains bounded by one host's capacity, not the sum of the cluster's memory.
