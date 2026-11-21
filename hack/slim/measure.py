#!/usr/bin/env python3
"""Run in the builder: measure.py QEMU_POD_IP RAM_MIB BOOT_EPOCH.

The guest is a fresh live node. SLIM_WORK holds media trust, private temporary
credentials and results; defaults to /work/slim. No commands run on the host
inside the guest except through the Kubernetes APIs.
"""
import base64
from datetime import datetime
import json
import os
from pathlib import Path
import statistics
import subprocess
import sys
import time

work = Path(os.environ.get("SLIM_WORK", "/work/slim"))
host, ram, boot = sys.argv[1], int(sys.argv[2]), float(sys.argv[3])
kubeconfig = work / f"cluster-{ram}.conf"
node = ["/work/bin/kubectl", "--kubeconfig", str(work / "trust/admin.kubeconfig"),
        "--server", f"https://{host}:50000", "--tls-server-name=kuberoot-node",
        "--request-timeout=5s"]
cluster = ["/work/bin/kubectl", "--kubeconfig", str(kubeconfig),
           "--tls-server-name=10.0.2.15", "--request-timeout=5s", "--namespace=default"]
report = {"ram_mib": ram, "stable": False}


def run(command):
    result = subprocess.run(command, capture_output=True, text=True, timeout=15)
    if result.returncode:
        raise RuntimeError(result.stderr.strip())
    return result.stdout


def get(arguments, client=cluster):
    return json.loads(run(client + arguments))


def save(label, data):
    (work / f"{ram}-{label}.json").write_text(json.dumps(data, indent=2) + "\n")


def poll(action, deadline):
    last = "condition not reached"
    while time.time() < deadline:
        try:
            value = action()
            if value:
                return value
        except (RuntimeError, subprocess.TimeoutExpired) as error:
            last = str(error)
        time.sleep(2)
    raise RuntimeError(f"deadline exceeded: {last}")


def ready_node():
    nodes = get(["get", "nodes", "-o", "json"])["items"]
    for item in nodes:
        if any(c["type"] == "Ready" and c["status"] == "True"
               for c in item["status"]["conditions"]):
            return item["metadata"]["name"]
    return None


def pods():
    return get(["get", "pods", "-A", "-o", "json"])


def addons_ready():
    items = pods()["items"]
    return (len(items) >= 2 and all(
        any(c["type"] == "Ready" and c["status"] == "True"
            for c in p["status"].get("conditions", [])) for p in items))


def memory(name, label):
    summary = get(["get", "--raw", f"/api/v1/nodes/{name}/proxy/stats/summary"])
    save(label + "-summary", summary)
    return summary["node"]["memory"]


def services():
    return get(["get", "nodeservices", "-o", "json"], node)


def latency(label):
    # Curl's timer excludes process startup and kubectl discovery. Verify the
    # certificate for the guest IP while connecting through QEMU's forwarding.
    config = get(["config", "view", "--raw", "-o", "json"])
    ca = next(c["cluster"]["certificate-authority-data"] for c in config["clusters"]
              if "certificate-authority-data" in c["cluster"])
    user = config["users"][0]["user"]
    for key, data in [("ca", ca), ("cert", user["client-certificate-data"]),
                      ("key", user["client-key-data"])]:
        path = work / f"{ram}-{key}.pem"
        path.write_bytes(base64.b64decode(data))
        path.chmod(0o600)
    samples, errors = [], 0
    for _ in range(30):
        try:
            code, elapsed = run([
                "curl", "--silent", "--show-error", "--max-time", "5",
                "--cacert", str(work / f"{ram}-ca.pem"),
                "--cert", str(work / f"{ram}-cert.pem"),
                "--key", str(work / f"{ram}-key.pem"),
                "--connect-to", f"10.0.2.15:6443:{host}:6443",
                "https://10.0.2.15:6443/api/v1/nodes",
                "-o", "/dev/null", "-w", "%{http_code} %{time_total}"]).split()
            if code != "200":
                errors += 1
            else:
                samples.append(float(elapsed) * 1000)
        except (RuntimeError, subprocess.TimeoutExpired):
            errors += 1
        time.sleep(0.2)
    result = {"requests": 30, "errors": errors, "samples_ms": samples}
    if samples:
        result.update(p50_ms=statistics.median(samples),
                      p95_ms=sorted(samples)[int(0.95 * (len(samples) - 1))],
                      max_ms=max(samples))
    save(label + "-latency", result)
    return result


def measure():
    credentials = poll(lambda: get(["get", "kubeconfigs", "admin", "-o", "json"], node),
                       boot + 180)
    kubeconfig.write_text(credentials["kubeconfig"])
    kubeconfig.chmod(0o600)
    report["node_api_s"] = round(time.time() - boot, 1)
    print("node API", report["node_api_s"], flush=True)
    name = poll(ready_node, boot + 360)
    report["ready_observed_s"] = round(time.time() - boot, 1)
    item = get(["get", "node", name, "-o", "json"])
    condition = next(c for c in item["status"]["conditions"] if c["type"] == "Ready")
    transition = datetime.fromisoformat(condition["lastTransitionTime"].replace("Z", "+00:00"))
    report["ready_s"] = round(transition.timestamp() - boot, 1)
    print("Ready", report["ready_s"], flush=True)
    poll(addons_ready, boot + 480)
    time.sleep(180)
    report["idle"] = memory(name, "idle")
    save("pods-idle", pods())
    before = services()
    save("services-idle", before)
    report["api_idle"] = latency("idle")
    print("idle", report["idle"], flush=True)
    for i in range(3):
        run(cluster + ["run", f"load-{i}", "--image=busybox:1.37.0", "--restart=Never",
                       "--", "sleep", "3600"])
    poll(lambda: sum(p["status"].get("phase") == "Running" and
                     p["metadata"]["namespace"] == "default"
                     for p in pods()["items"]) == 3, time.time() + 180)
    time.sleep(120)
    report["three_pods"] = memory(name, "three-pods")
    final_pods = pods()
    save("pods-loaded", final_pods)
    report["api_loaded"] = latency("loaded")
    meminfo = run(cluster + ["exec", "load-0", "--", "cat", "/proc/meminfo"])
    (work / f"{ram}-meminfo.txt").write_text(meminfo)
    values = {line.split(":")[0]: int(line.split()[1]) for line in meminfo.splitlines()
              if line.startswith(("SwapTotal:", "SwapFree:"))}
    report["swap_used_mib"] = (values["SwapTotal"] - values["SwapFree"]) / 1024
    try:
        stats = run(cluster + ["exec", "load-0", "--", "cat", "/sys/block/zram0/mm_stat"])
        report["zram_bytes"] = dict(zip(["original", "compressed", "backing"],
                                        map(int, stats.split()[:3])))
    except RuntimeError as error:
        report["zram_stats_error"] = str(error)
    after = services()
    save("services-final", after)
    save("nodes-final", get(["get", "nodes", "-o", "json"]))
    restarts = lambda data: {s["metadata"]["name"]: s["status"].get("restarts", 0)
                             for s in data["items"]}
    pod_restarts = sum(c.get("restartCount", 0) for p in final_pods["items"]
                       for c in p["status"].get("containerStatuses", []))
    report["pod_restarts"] = pod_restarts
    report["stable"] = (ready_node() == name and restarts(before) == restarts(after)
                        and not pod_restarts and not report["api_idle"]["errors"]
                        and not report["api_loaded"]["errors"])


try:
    measure()
except Exception as error:
    report["failure"] = str(error)
finally:
    report["elapsed_s"] = round(time.time() - boot, 1)
    save("report", report)
    print(json.dumps(report, indent=2), flush=True)
