#!/usr/bin/env python3
import json,sys,pathlib
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
rows=[json.loads(x) for x in pathlib.Path(sys.argv[1]).read_text().splitlines()]
rows=[r for r in rows if 'guest' in r and 'error' not in r]
plt.rcParams.update({'font.size':18})
fig,ax=plt.subplots(figsize=(9,6),layout='constrained')
x=[r['elapsed_seconds']/60 for r in rows]
for key,label in [('usageBytes','Usage (including cache)'),('workingSetBytes','Working set')]:
 ax.plot(x,[r['guest']['memory'][key]/1024**2 for r in rows],label=label,linewidth=2.5)
ax.set(xlabel='Idle sampling time (min)',ylabel='Guest memory (MiB)',title=sys.argv[3]);ax.legend(fontsize=14);ax.grid(alpha=.3)
fig.savefig(sys.argv[2],dpi=160)
