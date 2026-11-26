#!/usr/bin/env python3
"""Retime a real serial asciicast, retaining all events; render within GIF limits."""
import json,pathlib,subprocess,sys
s=pathlib.Path(__file__).resolve().parent;p=pathlib.Path(sys.argv[1]);stem=p.with_suffix('')
subprocess.run(['python3','/work/scripts/render.py',str(p)],check=True)
r=stem.with_suffix('.retimed.cast')
subprocess.run(['python3',str(s/'bound-cast.py'),str(r)],check=True,stdout=pathlib.Path(str(stem)+'-speed.json').open('w'))
lines=r.read_text().splitlines();duration=json.loads(lines[-1])[0] if len(lines)>1 else 0
subprocess.run(['/work/bin/agg','-q','--font-size','16','--theme','monokai','--last-frame-duration',str(max(5,15.1-duration)),str(r),str(stem)+'.gif'],check=True)
subprocess.run(['python3',str(s/'verify-capture.py'),str(p),str(stem)+'.gif'],check=True,stdout=pathlib.Path(str(stem)+'-verification.json').open('w'))
