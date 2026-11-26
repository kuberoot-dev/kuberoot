#!/usr/bin/env python3
"""Plot observed idle samples; no synthetic points or fabricated comparisons."""
import json,sys
from pathlib import Path
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
rows=[json.loads(line) for line in Path(sys.argv[1]).read_text().splitlines()]
valid=[r for r in rows if r.get('osconfigs',{}).get('items') and isinstance(r.get('blockstats'),list)]
if len(valid)<2:raise SystemExit('need two real samples')
x=[r['elapsed_s']/60 for r in valid]
y=[(r['osconfigs']['items'][0]['status']['memoryTotal']-r['osconfigs']['items'][0]['status']['memoryFree'])/2**20 for r in valid]
w=[sum(d['stats']['wr_bytes'] for d in r['blockstats'] if d.get('device')) for r in valid]
w=[(v-w[0])/2**20 for v in w]
plt.rcParams.update({'font.size':17})
fig,axes=plt.subplots(2,1,figsize=(8,9),layout='constrained')
axes[0].plot(x,y);axes[0].set_ylabel('Total − free, MiB');axes[0].set_title('kuberoot capture.1, 2 GiB VM\nIncludes cache; not MemAvailable',fontsize=17)
axes[1].plot(x,w);axes[1].set_ylabel('Guest writes, MiB');axes[1].set_xlabel('Observed time, minutes')
for a in axes:a.grid(alpha=.25)
fig.suptitle('R-B1 idle samples'+(' — incomplete hour' if x[-1]-x[0]<59.98 else ''),fontsize=19)
fig.savefig(sys.argv[2],dpi=150);plt.close(fig)
