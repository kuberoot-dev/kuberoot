#!/usr/bin/env python3
"""Run in the builder against a fresh running guest, after memory measurements."""
import json
import os
from pathlib import Path
import subprocess
import sys
import time

work = Path(os.environ.get("SLIM_WORK", "/work/slim"))
host = sys.argv[1]
node = ["/work/bin/kubectl", "--kubeconfig", str(work / "trust/admin.kubeconfig"),
        "--server", f"https://{host}:50000", "--tls-server-name=kuberoot-node",
        "--request-timeout=5s"]
config = work / "functional.conf"
cluster = ["/work/bin/kubectl", "--kubeconfig", str(config),
           "--tls-server-name=10.0.2.15", "--namespace=default", "--request-timeout=15s"]


def run(args, stdin=None):
    p = subprocess.run(args, input=stdin, text=True, capture_output=True, timeout=120)
    if p.returncode:
        raise RuntimeError(p.stderr)
    return p.stdout


for _ in range(90):
    try:
        credentials = json.loads(run(node + ["get", "kubeconfigs", "admin", "-o", "json"]))
        config.write_text(credentials["kubeconfig"])
        config.chmod(0o600)
        nodes = json.loads(run(cluster + ["get", "nodes", "-o", "json"]))
        if not nodes["items"]:
            raise RuntimeError("node not registered yet")
        break
    except (RuntimeError, subprocess.TimeoutExpired):
        time.sleep(2)
else:
    raise RuntimeError("guest API did not become ready")
run(cluster + ["wait", "node", "--all", "--for=condition=Ready", "--timeout=90s"])
manifest = {
    "apiVersion": "v1", "kind": "List", "items": [
        {"apiVersion": "v1", "kind": "PersistentVolumeClaim",
         "metadata": {"name": "smoke"},
         "spec": {"accessModes": ["ReadWriteOnce"],
                  "resources": {"requests": {"storage": "16Mi"}}}},
        {"apiVersion": "v1", "kind": "Pod", "metadata": {"name": "smoke"},
         "spec": {"restartPolicy": "Never", "containers": [
             {"name": "smoke", "image": "busybox:1.37.0", "command": ["sleep", "600"],
              "volumeMounts": [{"name": "data", "mountPath": "/data"},
                               {"name": "binaries", "mountPath": "/hostbin", "readOnly": True}]}],
             "volumes": [{"name": "data", "persistentVolumeClaim": {"claimName": "smoke"}},
                         {"name": "binaries", "hostPath": {"path": "/usr/bin", "type": "Directory"}}]}}]}
try:
    print(run(cluster + ["apply", "-f", "-"], json.dumps(manifest)), flush=True)
    # Separate short waits keep each kubectl call bounded.
    for _ in range(6):
        try:
            run(cluster + ["wait", "pod/smoke", "--for=condition=Ready", "--timeout=60s"])
            break
        except RuntimeError:
            continue
    else:
        raise RuntimeError("PVC test pod did not become ready")
    print(run(cluster + ["exec", "smoke", "--", "sh", "-ec",
                         "nslookup kubernetes.default.svc.cluster.local; "
                         "echo slim-pvc-ok >/data/check; test $(cat /data/check) = slim-pvc-ok; "
                         "for c in kube-apiserver kube-controller-manager kube-scheduler kubelet kube-proxy; "
                         "do /hostbin/$c --version; done; /hostbin/kine --help >/dev/null"]), flush=True)
    print(run(cluster + ["get", "pvc", "smoke", "-o", "wide"]), flush=True)
    print("FUNCTIONAL_OK", flush=True)
finally:
    print(run(cluster + ["delete", "pod/smoke", "pvc/smoke", "--ignore-not-found", "--wait=false"]), flush=True)
