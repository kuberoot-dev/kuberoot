#!/usr/bin/env python3
"""Charts derived exclusively from archived measured records."""
import json,statistics
from pathlib import Path
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
root=Path('/work/field');raw=root/'raw';img=root/'img'
plt.rcParams.update({'font.size':17})
values=[]
for name in ['r2','debian']:
 p=raw/f'R-B1-{name}-idle.summary.json'
 if p.exists():
  s=json.loads(p.read_text());values.append((name,s['disk_write_bytes']/2**20))
fig,ax=plt.subplots(figsize=(8,6),layout='constrained');ax.bar([x[0] for x in values],[x[1] for x in values],color=['#4169b1','#d88a35']);ax.set_ylabel('Guest logical writes, MiB/hour');ax.set_title('Measured idle hour\nr2: kuberoot capture.1; Debian 12')
for i,(_,v) in enumerate(values):ax.text(i,v,f'{v:.1f}',ha='center',va='bottom')
ax.set_ylim(0,max(x[1] for x in values)*1.2);fig.savefig(img/'chart-R-B1-observed-idle-writes.png',dpi=150);plt.close(fig)
boots=[json.loads(p.read_text()) for p in sorted(raw.glob('R-B1-debian-boot-??.json'))]
if boots:
 fig,ax=plt.subplots(figsize=(8,6),layout='constrained');ax.plot([b['trial'] for b in boots],[b['first_forward_s'] for b in boots],'o-',label='First forwarded reply');ax.plot([b['trial'] for b in boots],[b['stable_forward_s'] for b in boots],'s-',label='30 s sustained replies');ax.set_xlabel('Cold boot trial');ax.set_ylabel('Seconds after VM start');ax.set_title('Debian, 2 vCPU / 2 GiB\nPositive NAT probes; not boot firewall gate');ax.legend(fontsize=13);ax.grid(alpha=.3);fig.savefig(img/'chart-R-B1-debian-cold-boots.png',dpi=150);plt.close(fig)
rows=[json.loads(x) for x in (raw/'R-B2-linux-fabric-direct.jsonl').read_text().splitlines()]
labels=[];medians=[];low=[];high=[]
for reverse in [False,True]:
 vals=[r['iperf']['end']['sum_received']['bits_per_second']/1e6 for r in rows if r['mode']=='tcp' and r['reverse']==reverse]
 if vals:
  m=statistics.median(vals);labels.append('C → S' if not reverse else 'S → C');medians.append(m);low.append(m-min(vals));high.append(max(vals)-m)
fig,ax=plt.subplots(figsize=(8,6),layout='constrained');ax.bar(labels,medians,yerr=[low,high],capsize=8);ax.set_ylabel('TCP receiver, Mbit/s');ax.set_title('Direct QEMU socket fabric\nP8, 5 × 60 s, median and min–max');ax.set_ylim(0,max(medians)*1.3);fig.savefig(img/'chart-R-B2-direct-ceiling.png',dpi=150);plt.close(fig)
