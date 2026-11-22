#!/usr/bin/env python3
"""Temporary QEMU controller; run only inside hack/emulator/pod.yaml."""
from http.server import BaseHTTPRequestHandler, HTTPServer
import json
from pathlib import Path
import subprocess
import time
from urllib.request import urlretrieve

work = Path("/data")
for name in ("vmlinuz.efi", "initrd.cpio", "media.img"):
    urlretrieve("http://builder:8080/emulator/riscv64/" + name, work / name)
process = None


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        global process
        try:
            if self.path == "/start":
                if process is not None and process.poll() is None:
                    raise RuntimeError("a QEMU guest is already running")
                data = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                ram = data["profile"]
                if ram not in ("baseline", "emulator"):
                    raise ValueError("unsupported profile")
                with (work / f"final-{ram}.log").open("wb") as log:
                    boot = time.time()
                    process = subprocess.Popen(["sh", str(work / "run.sh"), str(ram)],
                                               stdin=subprocess.DEVNULL, stdout=log,
                                               stderr=subprocess.STDOUT)
                result = {"boot": boot, "pid": process.pid, "ram": ram}
            elif self.path == "/stop":
                if process is not None and process.poll() is None:
                    process.terminate()
                    try:
                        process.wait(timeout=15)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait()
                result = {"stopped": True}
            else:
                raise ValueError("unknown operation")
            self.send_response(200)
        except Exception as error:
            self.send_response(409)
            result = {"error": str(error)}
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps(result).encode())


HTTPServer(("0.0.0.0", 8090), Handler).serve_forever()
