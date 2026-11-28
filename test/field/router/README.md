# Router field tests

Scenarios that exercise the kuberoot router distribution the way a branch or
edge router is used, and run the same scenarios against VyOS, OpenWrt and a
hand-configured Debian router for comparison. Everything runs in QEMU guests
inside one Linux pod with KVM; nothing here touches the host network.

## Running

The scripts expect a pod with `/dev/kvm`, a persistent volume on `/work` and
`TMPDIR=/work/tmp`. `pod.json` is an example spec; it gets KVM from the KubeVirt device plugin
and runs under the `baseline` PodSecurity level. `prepare.sh` installs the tooling,
downloads the competitor images and records their digests.

Two helpers run on a workstation and reach the pod with `kubectl`:
`sync-baselines.py` copies finished idle windows out, and
`archive-observation.py` copies evidence for one scenario. Both read
`KUBECONFIG`, `FIELD_CONTEXT`, `FIELD_NAMESPACE`, `FIELD_POD` (default
`kr-router-stand`) and `FIELD_RESULTS`, the directory results are copied to.

Credentials generated for the guests stay on the volume under
`/work/field/private` and must never be copied into results.

## Topology

Every product is wired the same way, using unprivileged QEMU socket multicast
segments, each with its own group and port:

```
client 192.168.10.2 -> router (LAN .1, WAN 198.18.0.1)
  -> gateway (198.18.0.2, 198.18.34.1) -> sink (198.18.34.2, loopback 198.19.0.1)
```

The gateway VM injects WAN delay, jitter, loss, bandwidth limits and blackholes
with `tc netem`/`tbf` and nftables. A direct client-to-sink segment measures the
ceiling of the fabric itself. Management access goes through a separate
interface and is never the measured path. Throughput through userspace
segments is a relative comparison, not line rate: when every product hits the
same ceiling, compare CPU time per Gbit/s instead.

Storage faults use QEMU throttling and `blkdebug` or filling the guest
filesystem; power cuts use `kill -9` of QEMU or QMP `quit`; clock faults use a
per-VM `-rtc`.

## Scenarios

| ID | What it checks | Scripts |
|---|---|---|
| R-B1 | boot to forwarding, idle CPU, memory and disk writes | `R-B1-*.sh`, `idle.py`, `idle-linux.py`, `summarize-idle.py` |
| R-B2 | routed and NAT throughput | `forwarding.py`, `fabric-*.sh` |
| R-cap | which router features each product can configure | `R-cap-*` |
| R1 | a change that cuts off management is rolled back | `R1-*` |
| R2 | an upgrade that breaks forwarding or crashes is rolled back | `R2-*`, `build-fault-bundle.sh`, `fault-artifact-server.py`, `fault-image-server.py` |
| R3 | stale, conflicting and replayed configuration | `R3-*` |
| R4 | DHCP under load, lease persistence, VLAN isolation, release/retry, client IDs | `R4-*.py`, `dhcp-scapy.py` |
| R5 | NAT with many concurrent sessions | `R5-*.py`, `tcp-flows.py` |
| R6 | firewall with 100 to 10000 rules, edits, boot without leaks | `R6-*.py`, `linux-firewall-scale.py`, `tcp-port-probes.py` |
| R7 | full BGP table on small memory | `R7-*.py`, `bgp-prefix-generator.py` |
| R8 | DNS forwarding under load | `R8-*.py`, `dns-load.py` |
| R9 | BGP failure detection, flapping, carrier loss, daemon restart | `R9-*.py`, `udp-probes.py` |
| R10 | management API under load, RBAC, rejected unsafe changes | `R10-*` |
| R11 | disk writes per change, slow disk, full disk, I/O errors | `R11-*.py` |

`forwarding-observer.py` probes forwarding through the router during
lifecycle scenarios. A slot marked Good while forwarding fails counts as an
unsafe promotion, whatever the scenario expected.

## Recording

`capture.sh` records a scene with asciinema, keeps the command's exit code and
rejects a failed scene before rendering it to GIF. `console.py` and
`guest-command` propagate the guest command's exit code. `DEFER_RENDER=1`
records without rendering during benchmarks. `plot-*.py` draw the charts.

### Building the stand from scratch

`pod-setup.sh` installs the pod's tools, `build-in-pod.sh` builds the router
image from the checked-out tree (Go binaries in the pod, the root filesystem
with `scripts/build-rootfs.sh` in an Alpine chroot, the kernel from the
release, since `kernel/` did not change), and `linux-tools-base.sh` makes the
endpoints' base image. `r2-directboot.sh` lays out r2's disk the way the
installer does and boots it with the build's kernel, `r2-access.sh` fetches
its credentials (the node-admin kubeconfig a `kuberoot.dev` boot prints on
the console, the cluster admin from the node API), and `r2-config.yaml` is
its configuration on topology G. Then `fabric-vm.py` and
`fabric-configure.sh` for client, gateway and sink, and `r11-prepare.sh`.

### Separate state disk (r11)

`r11-prepare.sh` copies r2 into r11, renames the state partition in the copy
and moves `kuberoot-state` to a small disk that `qemu-storage-daemon` serves
over NBD, so the state store can be filled or failed without touching r2 or
the pod's volume. `R11-kuberoot-prefill.py` fills it offline with debugfs
(filling it through the API runs the API server out of memory first) and
`R11-kuberoot-state.py eio` kills the storage daemon for EIO.
`build-test-build.sh` builds a router image from a patched tree and
`r11-test-build.py` boots it on r11 to check a fix in the field.

`openwrt-rows.py` runs R2 (sysupgrade), R3, R8 and R9 (bird2 from the release
feed) on OpenWrt; `vyos-block.sh`, `debian-block.sh` and
`openwrt-reinstall-block.sh` run one product's remaining rows in order.

`render-deferred.sh` renders a scene recorded with `DEFER_RENDER=1`, speeding
playback up when it is longer than the GIF budget.

`result.py` appends one JSON row per measurement. A script being present does
not mean its scenario ran: only the recorded rows and raw output say so, and
an empty result is never a pass.
