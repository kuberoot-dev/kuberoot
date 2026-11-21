#!/usr/bin/env python3
"""Run in builder after server.py starts in the only emulator test pod."""
import json
from pathlib import Path
import subprocess
import sys
import time
from urllib.request import Request, urlopen
work=Path('/work/emulator')
host=sys.argv[1]
def control(action,body=None):
    request=Request(f'http://{host}:8090/{action}',data=json.dumps(body or {}).encode(),headers={'Content-Type':'application/json'},method='POST')
    with urlopen(request,timeout=60) as response: return json.load(response)
for profile in ('emulator','baseline'):
    start=control('start',{'profile':profile})
    print('START',profile,start,flush=True)
    try:
        with (work/f'{profile}-measure.log').open('w') as log:
            subprocess.run(['python3',str(work/'measure.py'),host,profile,str(start['boot'])],stdout=log,stderr=subprocess.STDOUT,check=True,timeout=7600)
        print('RESULT', (work/f'{profile}-report.json').read_text(),flush=True)
    finally:
        control('stop')
        print('STOP',profile,flush=True)
        time.sleep(3)
print('MATRIX_DONE',flush=True)
