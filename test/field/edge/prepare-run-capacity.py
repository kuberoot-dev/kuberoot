#!/usr/bin/env python3
"""Reclaim only disposable compiler cache, between measured E2 runs."""
import datetime,json,pathlib,shutil
root=pathlib.Path('/work/field-edge');cache=root/'build-main/gocache'
if shutil.disk_usage(root).free>=5*1024**3:raise SystemExit(0)
for p in pathlib.Path('/proc').glob('[0-9]*/cmdline'):
 try:args=p.read_bytes().decode().split('\0')
 except (OSError,UnicodeError):continue
 exe=pathlib.Path(args[0]).name
 if exe=='qemu-system-x86_64':
  assert '-name' in args and args[args.index('-name')+1]=='edge-field-gateway','DUT still running; no capacity cleanup'
 assert exe not in ['go','compile','link'],'Compiler still running; cache must stay intact'
if not cache.is_dir():raise SystemExit(0)
assert not cache.is_symlink() and cache.resolve()==cache
allocated=sum(p.stat().st_blocks*512 for p in cache.rglob('*') if p.is_file())
if allocated<128*1024**2:raise SystemExit(0)
stamp=datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ')
manifest=root/f'compiler-cache-retirement-{stamp}.json'
r={'utc':stamp,'path':str(cache),'allocated_bytes':allocated,'free_bytes_before':shutil.disk_usage(root).free,'reason':'reproducible Go build cache; no measured DUT/compiler active; source, module cache, binaries, images and evidence retained','removed':False}
manifest.write_text(json.dumps(r,indent=2))
shutil.rmtree(cache);cache.mkdir()
r.update(removed=True,free_bytes_after=shutil.disk_usage(root).free);manifest.write_text(json.dumps(r,indent=2));print(json.dumps(r),flush=True)
