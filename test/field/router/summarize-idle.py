#!/usr/bin/env python3
"""Summarize only observed samples, rejecting resets and incomplete hours."""
import json,statistics,sys
from pathlib import Path
p=Path(sys.argv[1]); rows=[json.loads(x) for x in p.read_text().splitlines()]
if len(rows)<2:raise SystemExit('need at least two samples')
a,b=rows[0],rows[-1]; elapsed=b['elapsed_s']-a['elapsed_s']
valid=[r for r in rows if isinstance(r.get('blockstats'),list) and r.get('osconfigs',{}).get('items')]
if len(valid)!=len(rows):raise SystemExit('sampling errors: retain raw, do not extrapolate')
boots={r['osconfigs']['items'][0]['status']['bootTime'] for r in rows}
if len(boots)!=1:raise SystemExit('guest rebooted during idle window')
def writes(row):return {v['device']:v['stats']['wr_bytes'] for v in row['blockstats'] if v.get('device')}
wa,wb=writes(a),writes(b)
if wa.keys()!=wb.keys() or any(wb[k]<wa[k] for k in wa):raise SystemExit('block counter reset/device changed')
deltas={k:wb[k]-wa[k] for k in wa}
mem=[r['osconfigs']['items'][0]['status']['memoryTotal']-r['osconfigs']['items'][0]['status']['memoryFree'] for r in rows]
summary={'elapsed_s':elapsed,'samples':len(rows),'observed_full_hour':elapsed>=3599,
 'disk_write_bytes_by_device':deltas,'disk_write_bytes':sum(deltas.values()),
 'extrapolated_write_bytes_per_day':sum(deltas.values())*86400/elapsed,
 'guest_total_minus_free_bytes_median':statistics.median(mem),'guest_total_minus_free_bytes_max':max(mem),
 'guest_memavailable_bytes':None,'note':'Total-minus-free includes cache; QMP counts guest writes, not physical flash write amplification. Shared pod also runs preparatory work.'}
path=p.with_suffix('.summary.json');path.write_text(json.dumps(summary,indent=2)+'\n');print(json.dumps(summary))
if not summary['observed_full_hour']:raise SystemExit('incomplete observed hour')
