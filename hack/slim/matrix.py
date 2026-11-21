#!/usr/bin/env python3
"""Run in builder after server.py starts in the QEMU pod; argument: pod IP."""
import json
import os
from pathlib import Path
import subprocess
import sys
import time
from urllib.request import Request, urlopen

work = Path(os.environ.get("SLIM_WORK", "/work/slim"))
host = sys.argv[1]


def control(action, body=None):
    request = Request(f"http://{host}:8090/{action}",
                      data=json.dumps(body or {}).encode(),
                      headers={"Content-Type": "application/json"}, method="POST")
    with urlopen(request, timeout=30) as response:
        return json.load(response)


for ram in (768, 512, 640, 1024):
    start = control("start", {"ram": ram})
    print("START", start, flush=True)
    try:
        with (work / f"measure-{ram}.log").open("w") as log:
            subprocess.run(["python3", str(work / "measure.py"), host, str(ram),
                            str(start["boot"])], stdout=log, stderr=subprocess.STDOUT,
                           check=True, timeout=900)
        result = json.loads((work / f"{ram}-report.json").read_text())
        print("RESULT", ram, json.dumps(result), flush=True)
    finally:
        control("stop")
        print("STOP", ram, flush=True)
        time.sleep(3)
print("MATRIX_DONE", flush=True)
