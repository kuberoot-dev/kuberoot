# Router field tests

Scenarios that exercise the kuberoot router distribution the way a branch or
edge router is used, and run the same scenarios against VyOS, OpenWrt and a
hand-configured Debian router for comparison. Everything runs in QEMU guests
inside one Linux pod with KVM; nothing here touches the host network.

## Running

The scripts expect a pod with `/dev/kvm`, a persistent volume on `/work` and
`TMPDIR=/work/tmp`. `pod.json` is an example spec; it needs a namespace whose
PodSecurity level allows privileged pods. `prepare.sh` installs the tooling,
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
| R2 | an upgrade that breaks forwarding or crashes is rolled back | `R2-*`, `build-fault-bundle.sh`, `fault-artifact-server.py` |
| R3 | stale, conflicting and replayed configuration | `R3-*` |
| R4 | DHCP under load, lease persistence, VLAN isolation | `R4-*.py` |
| R5 | NAT with many concurrent sessions | `R5-*.py`, `tcp-flows.py` |
| R6 | firewall with 100 to 10000 rules, edits, boot without leaks | `R6-*.py`, `linux-firewall-scale.py`, `tcp-port-probes.py` |
| R7 | full BGP table on small memory | `R7-*.py`, `bgp-prefix-generator.py` |
| R8 | DNS forwarding under load | `R8-*.py`, `dns-load.py` |
| R9 | BGP failure detection and flapping | `R9-*.py`, `udp-probes.py` |
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

`result.py` appends one JSON row per measurement. A script being present does
not mean its scenario ran: only the recorded rows and raw output say so, and
an empty result is never a pass.
