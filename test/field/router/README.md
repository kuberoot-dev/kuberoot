# Router field tests

Run only in the existing `tenant-kuberoot/kr-router-stand` workshop pod with
`TMPDIR=/work/tmp`. Workstation commands use `KUBECONFIG=~/workshop.txt kubectl
--context admin@workshop -n tenant-kuberoot`. No host Docker or local builds.

This branch is work in progress, not a completed suite. The binding plan is
`kuberoot-articles/test-suites/router.md`; chronological evidence is in
`kuberoot-articles/03-router/STAND_LOG.md`. Never infer pass from an empty result.

* `prepare.sh`: install tooling inside the pod, download competitors and record
  image digests. OpenWrt 24.10.8 checks the publisher's SHA256 list; VyOS is the
  explicitly development rolling build 2026.11.25-0017. Debian's resolved image
  is pinned by the saved digest before use.
* `phase1-vm.sh`: reuse the preserved phase-one installed disks, reduce DUT RAM
  to the plan's 2048 MiB, expose QMP. Its user-net topology is for preparatory
  measurements only, not a valid throughput benchmark.
* `qmp.py VM COMMAND [JSON]`: send a real QMP command, fail on QMP errors.
* `idle.py r2 3600`: record one observed hour, with per-sample wall/monotonic
  clocks, node API guest memory, service cgroups and QMP write counters. The
  guest only exposes MemFree, so it is not labeled MemAvailable.

`pod.json` is a **rejected proposal**, not an applied configuration: server-side
admission refused NET_ADMIN and tun hostPath under baseline PodSecurity.
Do not apply it or weaken namespace policy. The existing pod remains intact.

Preserve credentials on the PVC only. Do not commit kubeconfigs, keys or raw
Secret objects. Every result needs its precise build, topology and limitations.

## Current preparation state

The initial pod was evicted for its injected 2 GiB ephemeral-storage limit.
It was recreated unchanged on ws3, preserving the PVC; apt cache now lives on
`/work/apt-cache`, with no recommended packages installed. The old ws1 measurements
must not be silently combined with the new runs. CPU/memory limits remain 6/14Gi.

`debian-seed.sh` creates private SSH material on the PVC and cloud-init for the
Debian guest. Never copy `/work/field/private` into public artifacts.
`debian-gateway.sh` runs **inside that guest** and configures its own LAN,
nftables, dnsmasq and FRR. `openwrt-gateway.sh` is the equivalent UCI preparation.
These use user-net for preparation only; they are not approved throughput profiles.

`capture.sh` saves the child exit code independently of asciinema, rejects a failed
scene before rendering, and checks 15–60 seconds / <8 MiB. A real `exit 7` probe
was rejected with no GIF produced. `console.py` similarly propagates the guest
command's exit code. `R-B1-*.sh` are snapshots, not full R-B1 acceptance tests.

The background idle sample on r2 begins after 600 seconds of settling and observes
3600 seconds. `summarize-idle.py` rejects a short hour, any failed API samples,
reboots or reset block counters; `plot-idle.py` labels short windows incomplete.
QMP guest writes are not host/device flash write amplification. Service cgroups,
QEMU RSS and guest Total−Free are separate metrics; none is MemAvailable.

## Approved resume methodology

The author superseded the bridge/tap requirement: use identical unprivileged
QEMU socket multicast segments for every product, each with a distinct group
and port. WAN delay/jitter/loss/bandwidth/blackholes run via tc netem/tbf/nft
inside a dedicated Linux gateway VM. Management hostfwd is observation access;
measured probes must traverse the DUT. Measure direct client/sink and Linux
forwarding fabric ceilings first. Publish relative userspace-fabric throughput,
never line rate; if all products hit that ceiling, report DUT CPU per Gbit/s.
Storage faults use QEMU throttle/blkdebug or guest filesystem exhaustion; power
cuts use kill -9 or QMP quit; clock faults use per-VM -rtc. No pod capabilities,
namespace changes, host bridges or tun devices are needed or authorized.

`fabric-vm.py` creates 768 MiB Linux client/gateway endpoints from an offline
Debian tools image. Cloud-init binds NIC names by MAC (mgmt0/test0/direct0),
independent of PCI enumeration. `fabric-configure.sh` configures only those
guests. `fabric-competitor.sh` attaches each competitor to the same LAN/WAN.
`guest-command NAME COMMAND` runs the displayed command by SSH on its observer
interface; payload routing is determined inside the guest, not SSH.
`fabric-smoke.sh` checks connectivity and a 5-second direct iperf sample; this
is a setup smoke test, not the repeated ceiling benchmark or R-B2 result.
`idle-linux.py` settles 600 seconds then observes 3600 seconds, preserving guest
MemAvailable, QMP writes, CPU and boot identity; it refuses to overwrite a run.

The final G fabric adds a separate sink VM: client(192.168.10.2) →
DUT(LAN .1, WAN 198.18.0.1) → gateway(.2, 198.18.34.1) → sink(.2,
loopback 198.19.0.1). Segments use multicast ports 13031/13032/13034.
The direct ceiling path uses client↔sink segment 13033. Gateway delay is injected
on test0 (return direction); 50ms there adds approximately 50ms RTT, not 100ms.
Each endpoint currently has 768 MiB/1 vCPU. Observer SSH is never the measured
payload path. Earlier direct smoke used gateway itself as sink and is explicitly
a preparation result; it must not be reused as the final ceiling.

`build-main.sh` builds source commit 1b29543 on the stand PVC because the shared
builder disk was full. Kernel/external packages reuse the signed capture.5
payload; all project binaries, kubepkg, CRDs and source overlays are rebuilt/copied.
`finish-main.sh` completes the initially launched incremental build; inspect its
exit and build-manifest before using the resulting bundle.
`boot-debian.py` observes ten installed cold boots and 30 consecutive seconds
of positive NAT reachability. This does not assert the R6 negative-filter gate.
`R-B1-debian-serial-fabric.sh` records the first boot with the live serial console.
`forwarding.py` is prepared for later R-B2: 5×60s TCP P8 and UDP64 in each
direction, preserving iperf JSON and QEMU CPU time. It does not automatically
claim generator headroom or a pass; evaluate the full baseline and CPU limits.

`capture.sh` accepts AGG_FPS (default15); the live Debian serial cast was
rerendered at5fps to stay below8MiB, without dropping transcript lines.
`sync-baselines.py` runs on the Mac with only kubectl/file operations: it archives
completed r2/Debian idle windows and appends partial R-B1 rows/documents, never
converts them into a whole-suite pass. It cannot execute the remaining tests.

## Continued field run

`R1-address.py` changes the real production eth0 address under a 60-second
Safeguard and polls for a recorded rollback; `--reboot` injects QMP reset at
20 seconds. The observer does not repair the guest. `R1-stale-confirm.py`
submits revision N after N+1 is pending. `R3-conflicts.py` submits an obsolete
resourceVersion, and `R3-replay.sh` reapplies an unconfirmed route every 10 s.
These initial lifecycle phases use the existing phase-1 multicast client/peer
LANs and production user-net WAN. They are not final-G throughput comparisons.

`fault-artifact-server.py` serves actual bundle bytes with a declared full
length and a disconnect at 25%, or with a truncated declared length.
`build-fault-bundle.sh` produces explicitly named, signed lab images: crash,
forwarding disabled while the router stays alive, or a serial fault console.
The console variant is instrumentation for kernel/storage fault injection;
it is not a claimed native product management interface. Do not use its
footprint as an uninstrumented production baseline.

`forwarding-observer.py` probes c1 → r1 → r2 → c2 over the phase-1 multicast
LAN/transit segments and returns HTTP 200 only for a fresh successful reply.
`R2-upgrade-trial.py` records all boot states and contemporaneous forwarding
observations, plus the real serial log. A Good slot with failed forwarding is
an unsafe promotion, regardless of whether the harness expected rejection.

`forwarding.py` records full iperf JSON and QEMU CPU time. Its CPU interval
includes the 5-second warmup; throughput comes from iperf's measured interval.
The direct fabric's 80 Mbit/s offered UDP64 load lost packets; the 8 Mbit/s
retry is a separate offered-load experiment, not a discovered maximum PPS.
No fabric-limited result is labelled hardware line rate. `DEFER_RENDER=1`
records casts and transcripts without spending CPU on GIF rendering during
benchmarks. `/work/field/render.pause` defers ordinary capture rendering.

`test-idle-summary.py` uses synthetic fixtures only for regression tests.
The summary accepts one second of OSConfig bootTime rounding only when all
service PID/start/restart identities stay fixed and block counters remain
monotonic. It rejects actual boot changes, service restarts and counter resets.

Scripts being present does not mean their scenarios ran. Raw output and the
article's JSONL/RESULTS determine execution status. In particular, the TCP
flow, UDP probe and Linux firewall-scale drivers were prepared before their
first execution; never cite their defaults as measured results.
