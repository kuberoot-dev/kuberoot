#!/usr/bin/env python3
"""Run in the builder after functional.py; keep three pods alive for 20 minutes."""
import json
import os
from pathlib import Path
import subprocess
import sys
import time

work = Path(os.environ.get("SLIM_WORK", "/work/slim"))
k = ["/work/bin/kubectl", "--kubeconfig", str(work / "functional.conf"),
     "--tls-server-name=10.0.2.15", "--namespace=default", "--request-timeout=10s"]
node = ["/work/bin/kubectl", "--kubeconfig", str(work / "trust/admin.kubeconfig"),
        "--server", f"https://{sys.argv[1]}:50000", "--tls-server-name=kuberoot-node",
        "--request-timeout=10s"]
report = {"stable": False, "samples": []}


def run(args):
    p = subprocess.run(args, capture_output=True, text=True, timeout=20)
    if p.returncode:
        raise RuntimeError(p.stderr.strip())
    return p.stdout


def get(args, client=k):
    return json.loads(run(client + args))


def services():
    return {s["metadata"]["name"]: s["status"].get("restarts", 0)
            for s in get(["get", "nodeservices", "-o", "json"], node)["items"]}


try:
    for i in range(3):
        run(k + ["run", f"soak-{i}", "--image=busybox:1.37.0", "--restart=Never",
                 "--", "sleep", "3600"])
    deadline = time.monotonic() + 180
    while True:
        pods = get(["get", "pods", "-l", "run", "-o", "json"])["items"]
        if len(pods) == 3 and all(p["status"].get("phase") == "Running" for p in pods):
            break
        if time.monotonic() > deadline:
            raise RuntimeError("load pods did not start")
        time.sleep(2)
    before = services()
    started = time.monotonic()
    for minute in range(21):
        time.sleep(max(0, started + minute * 60 - time.monotonic()))
        tick = time.monotonic()
        nodes = get(["get", "nodes", "-o", "json"])["items"]
        api_ms = (time.monotonic() - tick) * 1000
        item = nodes[0]
        if not any(c["type"] == "Ready" and c["status"] == "True" for c in item["status"]["conditions"]):
            raise RuntimeError("node lost Ready")
        name = item["metadata"]["name"]
        memory = get(["get", "--raw", f"/api/v1/nodes/{name}/proxy/stats/summary"])["node"]["memory"]
        pods = get(["get", "pods", "-A", "-o", "json"])["items"]
        if any(p["status"].get("phase") != "Running" or
               any(c.get("restartCount", 0) for c in p["status"].get("containerStatuses", []))
               for p in pods if not p["metadata"]["name"].startswith("helper-pod-")):
            raise RuntimeError("a pod stopped or restarted")
        sample = {"minute": minute, "kubectl_get_nodes_ms": round(api_ms, 1), "memory": memory}
        report["samples"].append(sample)
        print(json.dumps(sample), flush=True)
    report["service_restarts"] = services()
    report["stable"] = report["service_restarts"] == before
    report["elapsed_s"] = round(time.monotonic() - started, 1)
except Exception as error:
    report["failure"] = str(error)
finally:
    (work / "768-soak.json").write_text(json.dumps(report, indent=2) + "\n")
    print("SOAK_RESULT", json.dumps(report), flush=True)
    subprocess.run(k + ["delete", "pods", "-l", "run", "--wait=false"], timeout=20)
