# Edge field tests — preparation checkpoint

The binding experiment plan is `kuberoot-articles/test-suites/edge.md`.
This branch starts at kuberoot `b365f53` (origin/main fetched 2026-11-25).
**The field suite has not run.** Do not interpret observer self-tests as product
results, and do not substitute Phase 1 user-network/qcow2 measurements.

## Current blocker

The existing `kr-edge-stand` pod has KVM but no NET_ADMIN or TUN device.
Server dry-run of `pod.json` was rejected by namespace PodSecurity baseline:
NET_ADMIN and the TUN hostPath are forbidden. An administrator-approved network
fixture is required. No namespace policy was changed; no pod was deleted.
The JSON manifest is a proposed fixture, not an instruction to bypass admission.

Use `KUBECONFIG=~/workshop.txt kubectl --context admin@workshop -n tenant-kuberoot`
for workshop access. All scripts and QEMU run **inside kr-edge-stand**;
Mac activity is restricted to source editing, git and kubectl.

## Files and verification

- `preflight.py --record`: read-only capability/tool/space checks, append to
  `/work/field-edge/preflight.jsonl`; exit 2 means blocked. This is not a field
  test outcome. Run with `/work/bin` on PATH.
- `lib.sh`: guarded 2-vCPU/4096-MiB/32-GiB raw `cache=none` UEFI QEMU launch,
  private bridge/TAP fixture, and command typing. It refuses missing prerequisites.
  The launch fixture has not been exercised because admission is blocked.
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
The next step is the approved network fixture, then product pinning/preload,
clean disk installation and E-B1, continuing in table order. Per-test orchestration,
full workload deployment, repetition accounting and GIFs remain to be implemented.
