# Edge field tests — preparation checkpoint

The binding experiment plan is `kuberoot-articles/test-suites/edge.md`.
This branch starts at kuberoot `b365f53` (origin/main fetched 2026-11-25).
**The field suite is in progress; no complete test row is available yet.** Do not interpret observer self-tests as product
results, and do not substitute Phase 1 user-network/qcow2 measurements.

## Network and storage method (author-approved revision)

All products use the same unprivileged QEMU socket multicast L2 fabric, with
unique multicast groups/ports per segment. Small Linux gateway VMs perform
routing, NAT, tc netem/tbf and nft blackholes inside the guest. Host NET_ADMIN,
TUN and namespace policy changes are neither needed nor requested. Observer
application probes traverse the DUT; management user-net hostfwd is separate.
Before forwarding comparisons measure direct client-to-sink and Linux-router
ceilings; results are relative to this fabric, never hardware line rate. If
fabric-limited, report DUT CPU cores per Gbit/s. Storage faults use QEMU throttle
groups, guest state filesystem filling and blkdebug EIO; power cuts kill QEMU;
clock tests use per-VM RTC. No host device-mapper is used.

Use `KUBECONFIG=~/workshop.txt kubectl --context admin@workshop -n tenant-kuberoot`
for workshop access. All scripts and QEMU run **inside kr-edge-stand**;
Mac activity is restricted to source editing, git and kubectl.

## Files and verification

- `preflight.py --record`: read-only capability/tool/space checks, append to
  `/work/field-edge/preflight.jsonl`; exit 2 means blocked. This is not a field
  test outcome. Run with `/work/bin` on PATH.
- `lib.sh`: guarded 2-vCPU/4096-MiB/32-GiB raw `cache=none` UEFI QEMU launch,
  userspace multicast fixture, and command typing. It refuses missing prerequisites.
  The revised launch fixture still requires live validation.
  Before three-VM tests stop the old Phase 1 VM; total guest RAM must fit 14Gi.
- `workload.py`: `/http` returns exactly 1024 bytes; `/append` accepts numbered
  1024-byte records and acknowledges only after fsync; `/records` exports bytes
  for post-recovery comparison. Run HTTP as ten replicas, append as one replica
  on its own persistent volume. Container pinning/preloading and deployment are
  still required; this file alone is not a deployed W workload.
- `observer.py`: outside-DUT 20 HTTP requests/s and 10 appends/s. Records UTC,
  monotonic timing, response correctness, failures and fsyncs its evidence file.
  Dispatch exhaustion is an explicit driver error, not reduced offered load.
  Use a new append data file and a new observer output per repetition; IDs start
  at zero. Continue the same observer across fault/recovery; do not restart it
  against existing records. API ConfigMap probes are not yet implemented.
- `check-records.py OBSERVER RECOVERED`: compares acknowledged IDs and exact
  contents, reports missing/corrupt/duplicate records. Unacknowledged successful
  writes are valid unknown outcomes and are not counted as acknowledged loss.
- `selftest.sh`: five seconds against a real local HTTP server, verifies 100
  HTTP replies and 50 durable acknowledgements, torn-tail recovery, and detection
  of deliberate loss. It tests the harness, not virtual power-loss durability.

The self-test passed inside the workshop pod. No image build or VM ran on Mac.
The next step is product pinning/preload,
clean disk installation and E-B1, continuing in table order. Per-test orchestration,
full workload deployment, repetition accounting and GIFs remain to be implemented.

## Revised fixture and measurements

`prepare-gateway.sh` boots a 512 MiB Debian bookworm VM. The verified official
cloud image lives under `downloads/`; NoCloud seed is a virtio disk because the
cloud kernel did not discover the SATA CD seed. `gateway-command.sh` executes
its displayed SSH command inside this guest. `wan-impairment.sh` applies clean,
blackhole, loss/jitter or 2 Mbit/s profiles there. Management SSH terminates on
the gateway, so a WAN blackhole does not silently block the local observer.

`prepare-kuberoot.sh` installs published v0.1.0 on a fresh raw 32 GiB disk,
with DHCP on the multicast LAN. After installation, detach the install medium
and start the installed disk. Preserve installation serial output first.
`E-B1.py` samples whole-node kubelet telemetry and QMP virtual-block write
counters every 30 seconds for 1800 seconds. `availableBytes` is kubelet's
availability metric, not a direct /proc/meminfo MemAvailable reading. Filesystem
used bytes refer to the kubelet-reported state filesystem, not both A/B slots.
`start-baseline.sh` waits for disk compaction to end and add-ons to become ready,
settles for 180 seconds, then records the baseline and appends its JSON result.
A missing sample makes the run incomplete; raw errors remain in the sample log.

`capture.sh NAME SCRIPT` records real typed commands with asciinema, retimes
idle gaps, renders GIF/PNG inside the pod and refuses GIFs over 8 MiB.
`qmp.py SOCKET COMMAND [JSON_ARGUMENTS]` makes an actual QMP call, reporting
errors. `deploy-workload.sh` requires PYTHON_IMAGE pinned by digest, deploys ten
HTTP replicas plus an fsync append service on a PVC. Deployment/observer trials
remain separate from completed suite results.

## Execution status and next stages

The userspace gateway and clean installed release DUT have been exercised.
The 30-minute E-B1 release collector completed 61/61 valid samples. Main is
built and its baseline is in progress; competitors have not yet been measured. New `E-B2.py`, `E-B3.py`, `E1.sh`, `prepare-k3s.sh`, `build-main.sh`,
`storage-stressor.py` and application workload deployment are implementations
awaiting their first runtime validation, not evidence that these tests passed.
The full table still requires execution in order, competitor runs, fault lanes
and per-test artifacts. Do not open the completion PR before that work is done.

Observer `--kubeconfig` enables a separate 0.2 Hz ConfigMap create/read/delete
loop. API probe failures do not masquerade as acknowledged application records.
For WAN impairment, ingress is redirected to IFB *inside the gateway guest*;
LAN egress must remain untouched so local service probes are not artificially
delayed by a WAN fault. Blackholes also block gateway upstream DNS queries.
`storage-stressor.py` uses guest filesystem fallocate; this consumes guest free
extents without writing 30 GiB of zeros onto the lab PVC. It tests ENOSPC, not
physical flash wear or host PVC exhaustion.

The shared builder PVC is full. `build-main.sh` builds in this pod after idle
sampling, using source commit 1b29543967ae6fce971f0bf818000a6b04374b52. It reuses
the unchanged release kernel/userland and the previously validated exact
kubepkg commit 22170c1213a4, rebuilds all kuberoot binaries and the initrd,
and signs with the existing private test-series key. Never publish that key.

W uses an explicit static, node-affine PV on `/var/lib/field-records` on all
single-node products. This avoids adding a provisioner only to Talos and makes
the persistence substrate explicit. The declared 1 GiB PV capacity is not a
filesystem quota. This local volume does not provide application failover in
HA; do not claim replicated append availability from etcd majority tests.
The API probe loop uses the `field` namespace and records its own errors.

`run-baseline-products.sh` is the current sequential baseline queue: wait for
release completion, gracefully stop it, build/install/sample main, then k3s,
then Talos. It stops on setup errors for diagnosis. `sync-baselines.py` performs
only light kubectl/file operations on the Mac and publishes completed records
and copied artifacts; matplotlib rendering stays inside the pod. Completing
this loop is only E-B1, not completion of the suite.

`E6.py` is an unexecuted RTC/API checkpoint driver. It deliberately does not
claim full E6 pass from API recovery alone: application recovery, TLS diagnosis,
CA expiry derivation and every manual recovery operation remain mandatory.

The block fixture was exercised with real scratch I/O on QEMU 7.2.22:
`check-block-fixture.sh` wrote data, enabled a 100 IOPS throttle, reopened the
raw node onto a blkdebug filter, observed a real EIO write failure, removed the
filter and verified successful writes/readback. This is a harness check, not E4.
`BLOCK_FAULTS=1 vm_start 1` selects the explicit file → raw → throttle graph;
`block-fault.py` changes limits and swaps the raw node's child to/from blkdebug.
No VM reboot or host block device manipulation is required to inject/relieve EIO.
The guest may still need recovery after its filesystem reacts to EIO; that is
an outcome to measure, not something the harness should conceal.
See the [QMP blockdev-reopen reference](https://www.qemu.org/docs/master/interop/qemu-qmp-ref.html#command-blockdev-reopen)
and [QEMU 7.2.22 blkdebug implementation](https://github.com/qemu/qemu/blob/v7.2.22/block/blkdebug.c).

`run-cold-boots.sh` runs E-B2 only after the four idle baselines. It caches the
pinned workload image, cleanly stops each golden disk, and creates a fresh raw
and NVRAM clone per repetition. Each repetition has its own real terminal GIF;
the first also records QEMU serial boot. Node Ready must have a fresh heartbeat.
Failed disks are retained. The Ubuntu NoCloud seed remains attached on later
boots to preserve disk order and datasource identity. `publish-boot.py` records
explicit artifact transfer paths; the Mac sync process publishes only after
copying them. These boot scenarios await runtime validation.

Density HTTPS probes use strict mTLS with the kubeconfig's TLS server name,
including when connecting through the localhost tunnel. They measure complete
fresh-connection HTTPS requests, not kubectl process startup. The observer's
ConfigMap operations similarly preserve strict TLS verification.
