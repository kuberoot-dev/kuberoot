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
