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
