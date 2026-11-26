#!/usr/bin/env python3
"""Bound playback after idle capping; preserve every event and the original cast."""
import argparse,json,pathlib
p=argparse.ArgumentParser();p.add_argument('cast',type=pathlib.Path);p.add_argument('--max-seconds',type=float,default=50);a=p.parse_args()
assert a.cast.name.endswith('.retimed.cast'),'never rewrite an original recording'
rows=[json.loads(x) for x in a.cast.read_text().splitlines()];last=max((r[0] for r in rows[1:]),default=0);factor=min(1,a.max_seconds/max(last,.001))
if factor<1:
 for r in rows[1:]:r[0]*=factor
 a.cast.write_text(''.join(json.dumps(r,ensure_ascii=False)+'\n' for r in rows))
print(json.dumps({'before_seconds':last,'after_seconds':last*factor,'uniform_speed_factor':factor,'all_events_retained':True}))
