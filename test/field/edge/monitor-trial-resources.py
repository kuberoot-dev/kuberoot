#!/usr/bin/env python3
"""Read-only host resource evidence for one existing trial, until result.json."""
import argparse,datetime,json,pathlib,shutil,time
p=argparse.ArgumentParser();p.add_argument('directory',type=pathlib.Path);a=p.parse_args()
assert a.directory.is_relative_to('/work/field-edge/results') and a.directory.is_dir()
with (a.directory/'host-resources.jsonl').open('x') as f:
 while True:
  r={'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'pvc_free_bytes':shutil.disk_usage(a.directory).free}
  for name in ['memory.current','memory.events']:
   r[name]=(pathlib.Path('/sys/fs/cgroup')/name).read_text().strip()
  f.write(json.dumps(r)+'\n');f.flush()
  if (a.directory/'result.json').exists():break
  time.sleep(5)
