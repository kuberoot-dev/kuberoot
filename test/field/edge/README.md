# Edge field tests — preparation checkpoint

The binding experiment plan is `kuberoot-articles/test-suites/edge.md`.
This branch starts at kuberoot `b365f53` (origin/main fetched 2026-11-25).
**The field suite is in progress: E-B1, E-B2 and E-B3 have completed on all four products; E1 is running.** Do not interpret observer self-tests as product
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
The 30-minute E-B1 release collector completed 61/61 valid samples. Main also completed
61/61 samples. k3s is installed; competitor baselines have not yet completed. New `E-B2.py`, `E-B3.py`, `E1.sh`, `prepare-k3s.sh`, `build-main.sh`,
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

The strict-TLS client and observer were checked against the prepared k3s API
and a real harness HTTP/fsync service respectively: 100 HTTP requests, 50 append
acks, 50 recovered records, no corrupt/duplicate/missing records. This is not W
running on the DUT and is not an E1/E3 pass. Gateway IFB/netem/tbf setup and cleanup
were exercised inside the Linux VM, with actual packet counters; no pod network
capabilities were requested. Realized impairment and product recovery remain
scenario measurements.

`archive-stopped-disk.py SOURCE DESTINATION` streams a stopped owned Phase-1 disk
to a Mac archive to relieve the 40 GiB PVC. It refuses attached disks, hashes the
stream, rechecks the source hash, writes a manifest, and only then removes the
source inside our pod. Restoring that file with kubectl cp restores the original
bytes. Never archive an active disk or another workload's files. The Mac runs
only a file transfer and SHA256, not VM or build tools.

`serial-follow.py` reads the actual QEMU serial file, follows truncation on
restart, and runs `date -u` every 30 seconds and at the boundaries. Its stop file
ends the capture cleanly; no synthetic console events are inserted. E-B2/E3/E1
use it for serial GIFs. `E4.py` implements individual fresh-clone storage/W
subcases (100/20 IOPS, 90/95/100% fullness, EIO). It is not yet runtime-validated
and deliberately labels a W-only success as a subcase: upgrade safety must be
checked separately before E4 as a whole can pass. The guest-local ENOSPC
injector is a critical-priority pod with an independent HTTP relief endpoint;
it mounts the DUT state directory, never the workshop host/PVC.

Talos 1.14 generates multi-document configs. The single-node patch removes the
control-plane NoSchedule taint in `KubeNodeConfig` and removes `HostnameConfig.auto`
with `$patch: delete` before setting the static hostname. Legacy v1alpha1 fields
conflict with generated documents; `auto: null` does not delete the default.
The rejected configurations were preparation failures, not baseline results.
See [Talos 1.14 workload scheduling](https://docs.siderolabs.com/talos/v1.14/deploy-and-manage-workloads/workloads-on-controlplane)
and [HostnameConfig](https://docs.siderolabs.com/talos/v1.14/reference/configuration/network/hostnameconfig).

For Talos's default `console=tty0`, `vga-capture.py` additionally records the real
QEMU framebuffer and its UTC/monotonic frame timing. Serial captures remain
available and are not filled with invented kernel output. `bound-cast.py` caps
long playback after idle retiming by scaling timestamps uniformly; it preserves
every event and never rewrites the original asciicast.

The test-only `fixture-agent.go` refuses to run outside an installed kuberoot
root. Fault bundles use it for a crashing init service, volatile nft pod/DNS
drops, a volatile wrong-content HTTP redirect, and an incompatible rewrite of
the dedicated W record file. `build-fault-bundles.sh` signs them with the existing
lab key only after matching the baked trusted public key. These bundles and
`E2-kuberoot.py` still await runtime execution; they are not production changes
or evidence that rollback/application health gates work. The `/health` endpoint
on W can expose incompatible record formatting without changing its append/HTTP
contract. An OS rollback cannot undo arbitrary persistent-data migrations.

Cold-boot measurement protocol `boot-readiness-v2` waits for DNS and the new
NodePort response until the common 300-second power-on deadline. A Ready pod
alone does not imply CoreDNS/Service forwarding is ready. All unsuccessful DNS
attempts retain timestamps, return codes and output. The first three release
runs used an erroneous one-shot four-second exec timeout; those records and
forensic disks remain, and `rerun-boot-fixture-failures.sh` repeats them into
separate paths before density. Statistical summaries use v2 when available;
the original failed attempts remain individually visible. The aggregate PNG
contains measured milestones only; missing values are not assigned a time.

`prepare-ha-kuberoot.sh` follows the real media/Installation/JoinTicket flow in
`hack/e2e/etcd-ha.sh`. `ha-vm.sh` allocates three separate raw 32-GiB disks, fresh
OVMF vars, 2 vCPU/4 GiB per control plane, with a 13-GiB aggregate guest guard.
The pod still has its shared six-CPU quota (three VMs are not independent hosts).
Peer addresses 10.79.0.11–13 only exist on multicast 230.79.0.1:17901. Separate
user-net ports expose authenticated APIs for observation during a peer split.
`partition-etcd.py` performs real ConfigMap writes to all three endpoints,
isolates the observed leader with QMP set_link, reboots it, heals, then isolates
all three. It checks every acknowledged value after healing. These are quorum
assertions, not claims that a local application PVC replicates across nodes.
The HA driver is preparation until its actual result and captures are published.

Talos bad-release preparation uses its documented extension-service mechanism:
[extension services](https://docs.siderolabs.com/talos/v1.14/build-and-extend-talos/custom-images-and-development/extension-services),
[system extension packaging](https://docs.siderolabs.com/talos/v1.14/build-and-extend-talos/custom-images-and-development/building-a-system-extension).
`build-talos-faults.sh` extracts the pinned vendor imager binary and boot assets,
then uses `input.systemExtensions[].ociPath` from the v1.14.7 imager API. This
avoids requiring Docker, mounts, or additional pod privileges. The extension is
explicitly test-only; its process refuses non-kuberoot guests unless QEMU carries
the `field-edge-fault-test` SMBIOS serial. Its nft rules are volatile and its
persistent mutation is confined to the workload's own record file. All image
manifests/layers are hash-verified and pins retained. Preparation is not evidence
of a product result; images must actually build, install and demonstrate the
specified fault before any E2 outcome is assigned.

The guest ENOSPC injector reserves unwritten filesystem extents with fallocate;
it does not intentionally write a disk full of zeroes. E4 also monitors free
space on the owned PVC during fill and cancels its own filler below 768 MiB.
A host-capacity cancellation is a fixture limitation, not a DUT failure.

Failed cold-boot disks are preserved as qemu-img-verified compressed deltas
against `cold-boot-forensic-base.qcow2`. That backing snapshot is independent of
the original golden raw disk, so later workload preparation cannot invalidate
forensic images. Successful cold-boot clones are disposable; their raw measured
outputs and complete captures remain. No running VM disk is compressed.

`clock-drift-trials.py` records the requested RTC separately from observed guest
wall time (HTTP Date, guest date where supported, serial/VGA). Each case blocks
WAN/NTP, preserves strict certificate verification, corrects RTC by power cycle,
and requires both W and authenticated ConfigMap operations to recover. Ubuntu
may require an explicitly logged SSH `date` correction if persisted time defeats
the RTC change. The public server-CA expiry is read from the actual kubeconfig;
private keys are never emitted. Every manual recovery remains in the result.

Fault evidence is mandatory: E2 records the injected service log and verifies
wrong HTTP bytes or failed workload connectivity while the Kubernetes API still
answers. A missing/nonfunctional injector is `invalid-fixture`, never a product
rollback pass. Network faults affect both forwarded pod traffic and host-origin
workload/DNS traffic; wrong-content NAT targets only the HTTP NodePort in both
prerouting and output. The append NodePort and API port remain separate.

After the four real E1 windows, `after-wan-prepare.sh` waits for their queue to
finish before compiling images. Capacity preparation removes only stopped
Phase-1 installer media whose every boot payload and trust file matches retained
source artifacts. The public manifest retains original hashes, loader entries,
and exact recreation commands; guest disks and credentials stay intact. FAT/GPT
metadata of recreated media can differ. No cleanup runs during a measurement.

## Current checkpoint (2026-11-26)

The preceding preparation notes preserve implementation history; the authoritative
per-run outcomes are `results/edge.jsonl` and the article's `RESULTS.md`. All four
products now have 61 idle samples, ten valid v2 cold boots and density curves.
Density stopped at the default 110-pod scheduling limit (107 workload pods on
kuberoot/k3s, 103 on Talos), not demonstrated memory exhaustion. W and the
authenticated ConfigMap observer are deployed and exercised.

The release E1 run failed the strict zero-pre-reset-error criterion (18 HTTP/append
timeouts); all acknowledged records survived, with 39.645 s recovery. This is
preserved as a failed observation. Cumulative UDP receive-buffer drops on the
multicast fabric need correlation before attributing the timeouts to a product.
`fabric-monitor.py` samples network counters and individual multicast socket
drops without changing the active fixture. No heavy build runs during these
measurements.

`prepare-negative-bundles.sh` creates distinct tampered and untrusted-signature
fixtures. The tampered bundle retains its original signature but gets a fresh
transport SHA256; the untrusted bundle has identical payload bytes and a
different signing key, absent from the guest. Neither case bypasses validation.
Private test keys stay in the owned PVC and are excluded from publication.

The E2 kuberoot lane runner uses ten fresh clones per fault/trust case. It stops
on an invalid injector, preserves all product failures, and snapshots stopped
trial disks against an immutable forensic base. A bad network/content fixture
keeps its init process alive; otherwise an accidental injector crash would
confound application-health testing with init-service health. The release
test-key lane is labelled separately from the published release, using the
Phase-1 repack with unchanged release binaries and an explicit lab trust root.
`bundle-server.py` serves only public bundle/signature/archive names and logs
actual transmitted bytes and Range requests for later maintenance-window tests.

`after-wan-prepare.sh` waits for all four E1 captures and the WAN queue to finish,
then audits the full WAN windows, prepares capacity and builds only the main
fault lane before running its E2 trials. It does not prebuild all competitors
on the shared 40-GiB PVC. Future lanes remain required; queue completion is not
suite completion. `prepare-ha-k3s.sh` prepares three actual embedded-etcd servers
on fresh Ubuntu raw disks, binds peer/advertise addresses to the peer-only LAN
and records the observed leader through authenticated etcd metrics. It still
requires runtime validation in E5. No secrets are printed or published.

The E1 evaluator now checks both pre-reset continuity and the remaining WAN
window after the first sustained 60-second recovery; whole-window audits retain
every failed request. The original release run is already failed and has a
supplementary audit under the same criterion. `chart-wan.py` plots actual
per-minute failures and client latency, including timeouts, alongside the real
power-cut timestamp.

`upgrade-power-phase.py` is an unexecuted E3 hook: it observes an actual Upgrade
phase, waits the seeded offset, confirms the phase again and pauses QEMU before
the recorded kill. Both pause and kill timestamps are retained; recovery starts
from the earlier effective outage. Download shaping and a 5-MiB/s write cap make
the respective phases observable; both are confined to the fixture. A missed
phase is not counted as a successful injection. The other products need their
own real upgrade hooks before upgrade-phase coverage can be claimed.

`storage-churn.py` implements the S07 background load: 50 pods each write about
1 KiB/s to stdout, and one oldest Ready pod is replaced every ten seconds.
Its actual ready counts, replacement commands and errors are retained. These
pods are additional to W and system components. E4 still needs live execution
and its upgrade-safety checks; a script's presence is not coverage.

`s3-presign.py` creates private SigV4 GET URLs without printing their query
strings. Against the real rclone S3 fixture, exact object bytes were returned
for a valid URL and a modified signature received HTTP 403. This enables E8 to
fetch directly from S3 instead of exporting an object to an unrelated HTTP
server. StateBackup/restore itself has not yet run in this suite.

E1's zero-error continuity gate is a deliberately strict laboratory interpretation
of local-service survival. Report the observed error rate and each recovery,
durability and missing-image gate separately; isolated connect timeouts do not
establish total service loss or its cause. `capture-tcp-handshakes.py` captures
only SYN/RST headers for W's two ports inside the gateway VM, never API traffic
or application payload. It changes no network settings.

## Corrected gateway fixture (2026-11-26, 09:31 UTC)

The 512-MiB Debian gateway defaulted to only 4096 conntrack entries. Real kernel
logs show table-full packet drops from 06:39 onward, overlapping the release,
main and partial k3s E1 observations. Those measurements are retained with
explicit `wan-gateway-v1-invalid` fixture-audit records; they cannot establish
a DUT defect. The k3s run was stopped after diagnosis, with its partial capture
and disk preserved. Talos was not run under the known-broken gateway.

The fix is entirely inside the gateway VM: persist
`net.netfilter.nf_conntrack_max=65536`. All products repeat the same full hour as
`E1-gateway-v2-*`, using fresh raw clones and unchanged W/fault timings.
`gateway-monitor.py` samples occupancy/limit/drop counters each second; any new
conntrack drop invalidates the run. No host sysctl, namespace capability or
network label changes are involved. E2 waits for all four corrected E1 runs.

The TLS registry preparation uses the validated `controlplane-fixed.yaml`, which
retains the static edge-talos hostname and scheduling configuration; the older
generated controlplane file and intermediate without-legacy file are not the
active config. The registry CA is a laboratory transport CA, not an image
signature policy. The real Distribution registry/cosign preparation script is
unexecuted and remains separate from completed product tests.

E2 Talos now has a real installer-extension driver and separate corrupt-blob / foreign-cosign-image fixtures. Only the lab transport CA is configured; default image authorization remains unchanged. The driver records extension logs, HTTP fault proof, boot IDs, actual upgrade output, serial/VGA and full boot-partition digests for negative cases. Runtime validation is pending; prepared code is not a completed test.

`capacity-at-wan-boundary.py` pauses only the queue parent after a measured run and ACPI shutdown, while its compressor finishes. `deduplicate-forensic-disks.py` preserves historical failed disks as verified qcow2 deltas against an immutable first failed disk, comparing all logical bytes before replacing the old standalone file. The backing file must never be deleted.

The prepared k3s E2 lane uses a fresh Ubuntu installation of official v1.37.1+k3s1 (SHA256 `92b94582eb7b34a8cf532e6006842b9ed215a6783cf56a3dd6271fd35243b2c0`) as A, then the unchanged official baseline v1.37.2+k3s1 binary as B. It never downgrades an existing datastore. A declared laboratory Debian package carries B and the intentional systemd service; this is not an upstream k3s Debian package and does not claim an Ubuntu kernel update. The actual APT verifier handles a foreign-signed repository and a corrupted package. Only the laboratory trusted repository public key is added. Native defaults for unattended-upgrades remain enabled. Runtime validation is pending.

E3/E4/E5 queue wrappers now publish each completed run before advancing. They are prepared, not yet executed. E3 release upgrade phases use the explicitly named release-testkey lane: GitHub v0.1.0 assets contain boot files and SHA256SUMS, but no signed upgrade bundle. Competitor upgrade-phase hooks and E4 upgrade-safety hooks still require implementation/runtime validation; writes-only or storage/W results must never be labelled full-row completion.

Prepared E8 restores control-plane state directly from an encrypted S3 object using a private presigned GET URL; app records have an independently encrypted snapshot and declared RPO. Negative inputs target an existing good disk, then check boot partition digests and whether the original workload still starts. The old source disk is quarantined offline, not used by the restore. Private manifests, age identities and signed URLs are excluded; serial publication explicitly redacts query strings/private identities while original evidence remains on the PVC. Runtime validation, old-ticket replay and the Talos snapshot reference are still pending.

Bulk completed observer samples and record files are gzip-compressed by the publisher, with decompressed SHA256 equality checked before unlinking the original. Plain command/output `.txt` transcripts are always retained uncompressed. `gzip -dc raw/<name>.jsonl.gz` recovers exact original samples; no observations are dropped. This bounds local and PVC usage across hundreds of trials.

Prepared E9 uses two real 600-second connectivity windows separated by a real hour, with a 2 Mbit/s gateway cap. Actual signed bundle size is recorded; unreachable requested 50/90% cuts are reported rather than fabricated. IPv4 forwarded byte counters and product download progress are separate metrics. Talos window comparison remains to be implemented/validated.

Prepared E10 runs three real sites behind separate cloned Linux gateway VMs and distinct multicast groups. Sites have 3 GiB each, center datastore VM 2 GiB, four gateways including the retained shared fixture 512 MiB each: 13 GiB guest total. Two supervised native fleet-manager processes run in the owned pod against the center datastore. Their Lease takeover is a process-failure comparison within one physical failure domain. Guest vCPUs share the pod 6 CPU quota. Runtime checks include canary stopping, center VM restart, active manager kill, independently authenticated restricted inventory reader denied node proxy, and offline-site revocation/reconnect. E10 has not yet run.

### E7 replacement and identity audit

`run-replacement-tests.sh` reuses the stopped three-control-plane fixtures from
E5 and creates a separate Talos three-control-plane fixture. The append volume
is explicitly pinned to surviving node 1. `ha-vm.sh` uses multicast for peer
traffic and QEMU user-net port forwards for observation. Removing the node-3
6443 forward is an **API-path** fault, not a claim that kube-apiserver stopped.
No workshop namespace labels or pod privileges are changed.

`replace-kuberoot-node.sh`, `replace-talos-node.sh` and
`replace-k3s-node.sh` retain the stopped original disk, install a new disk with
new management/peer MACs, and use the respective node API or declared cloud-init
provisioning. The k3s lane explicitly reuses its join token; the Talos lane reuses
cluster secrets. Neither reuse is presented as credential rotation. Command and
credential-type inventories are public; private configs remain on the PVC.

`node-identity-probe.py collect` uses a declared diagnostic Pod with a read-only
hostPath to collect the real old kubelet client certificate privately. This is
privileged diagnostic access through the guest Kubernetes API, not an SSH repair
shell. Only the **guest** `field` namespace permits those diagnostic Pods.
After removal, `probe` sends a real SelfSubjectReview with the retained credential.
Successful authentication disproves revocation; network failure or mere RBAC
Forbidden does not prove it. Keys and certificates are excluded from publishing.
`k3s-membership-audit.py` reads the authenticated embedded-etcd JSON gateway
through another diagnostic Pod; Kubernetes Node count alone is insufficient.

The Talos 1.14 HA config uses the schemas for
[LinkConfig](https://docs.siderolabs.com/talos/v1.14/reference/configuration/network/linkconfig),
[LinkAliasConfig](https://docs.siderolabs.com/talos/v1.14/reference/configuration/network/linkaliasconfig),
[KubeNodeConfig](https://docs.siderolabs.com/talos/v1.14/reference/configuration/kubernetes/kubenodeconfig)
and [KubeFlannelCNIConfig](https://docs.siderolabs.com/talos/v1.14/reference/configuration/kubernetes/kubeflannelcniconfig).
Merged configuration is validated by the pinned `talosctl` before apply.
These scripts are preparation until their actual result records exist.

E3's k3s APT lane uses `prepare-k3s-good-package.sh` and
`k3s-power-phase.py`: the unchanged official B binary is packaged without a fault
agent. Its actual phases are partial APT download, partial dpkg `.dpkg-new`
write, installed binary before reboot, and a verified new boot ID with uptime
below 120 seconds. k3s is not assigned fictitious inactive slots or probation.
Every seeded cut freezes a twice-observed phase using QMP, records that pause as
the outage start, then kills QEMU. This laboratory package is not an upstream
k3s Debian distribution; the Ubuntu kernel remains unchanged.

E8's `replay-preloss-membership.py` boots and installs a separate blank worker
with MAC `52:54:00:78:00:02`, then applies an unused 24-hour Membership issued
before the source snapshot. The restored control plane remains online. A real
new Node demonstrates acceptance; an unavailable API/timeout does not prove
rejection. The initial source disk remains quarantined. This tests the saved
Membership credential class, not all conceivable copied credentials. The
second guest uses the same userspace LAN; a DHCP reservation is configured
inside the owned gateway VM. The replay's serial capture and public result are
published separately from private membership material.

`E8-talos.py` is the native Talos etcd-snapshot reference. It streams a real
snapshot to external local S3, shuts down and quarantines the source, then boots
a blank replacement and calls `talosctl bootstrap --recover-from` with normal
hash checking. It measures snapshot-marker recovery and restores application
data from a separately captured snapshot. Native etcd snapshot data is plaintext
and stays in private fixture storage; only its size/hash and public results are
published. Machine configuration and snapshot secrets never enter transcripts.

Talos E3 uses `talos-power-phase.py` with the official unchanged v1.14.7
installer mirrored by digest. This is an explicitly declared same-version
reinstall, not a fabricated release. It verifies native installer output for UKI
writes, reads the actual EFI LoaderEntryDefault for selection changes, and uses
the real Upgrade META tag `0x06` plus changed boot ID for fallback-pending boot.
Talos removes that tag on Running+Ready; this is not kuberoot's probation timer.
The implementation is checked against the pinned upstream
[upgrade CLI](https://github.com/siderolabs/talos/blob/55021d7c274f6bc207bf18efa2a684a31a35cd9f/cmd/talosctl/cmd/talos/upgrade.go),
[sd-boot implementation](https://github.com/siderolabs/talos/blob/55021d7c274f6bc207bf18efa2a684a31a35cd9f/internal/app/machined/pkg/runtime/v1alpha1/bootloader/sdboot/sdboot.go)
and [fallback controller](https://github.com/siderolabs/talos/blob/55021d7c274f6bc207bf18efa2a684a31a35cd9f/internal/app/machined/pkg/controllers/runtime/drop_upgrade_fallback.go).
These references define observation hooks, not test results.

Forensic disk sharing uses `share-disk-blocks.py`: create an empty qcow2 overlay
on the stopped source, **safe rebase** it onto an immutable reference so QEMU
materializes only differing clusters, compress the resulting overlay, then
compare every logical byte with the original. `qemu-img convert -B` by itself
retains allocated source clusters and did not reduce these historical disks.
The helper never removes original evidence. Callers replace/unlink originals
only after successful comparison; backing files remain permanently retained.
A real 2 MiB fixture with a 4 KiB mutation passed round-trip comparison in the
pod and produced a 270336-byte allocated delta.

Terminal PNGs show the last 30 actual wrapped transcript lines. Full command
output is always retained in the corresponding raw text file; the bounded PNG
avoids a many-thousand-pixel scrolling screenshot.
