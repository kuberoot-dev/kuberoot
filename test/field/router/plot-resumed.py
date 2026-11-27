#!/usr/bin/env python3
import json,re,statistics
from pathlib import Path
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
raw=Path('/work/field/raw');img=Path('/work/field/img');plt.rcParams.update({'font.size':17})
a=[json.loads(p.read_text()) for p in sorted(raw.glob('R2-powercut-*.summary.json')) if '01.summary' not in p.name]
fig,ax=plt.subplots(figsize=(9,6),layout='constrained')
for field,label in [('first_api_s','Node API'),('first_forward_s','Forwarded reply')]:ax.plot([r['trial'] for r in a],[r.get(field) for r in a],marker='o',label=label)
ax.set(xlabel='Power-cut trial (01 excluded: manual restart)',ylabel='Observed recovery, seconds',title='kuberoot field.3: slot write / boot selection');ax.grid(alpha=.25);ax.legend();fig.savefig(img/'chart-R2-powercut-recovery.png',dpi=150);plt.close(fig)
fig,ax=plt.subplots(figsize=(9,6),layout='constrained')
for ram in ['2','4','8']:
 xs=[];ys=[]
 for p in sorted(raw.glob('R7-debian-*-'+ram+'GiB.json')):
  match=re.fullmatch(r'R7-debian-(\d+)-'+ram+r'GiB.json',p.name)
  if not match:continue
  values=[]
  for r in json.loads(p.read_text()):
   total=re.search(r'MemTotal:\s+(\d+)',r.get('text',''));avail=re.search(r'MemAvailable:\s+(\d+)',r.get('text',''))
   if total and avail:values.append((int(total[1])-int(avail[1]))/1024)
  if values:xs.append(int(match[1])+(200000 if int(match[1])==800000 else 0));ys.append(max(values))
 if xs:ax.plot(xs,ys,'o-',label='DUT '+ram+' GiB')
ax.set_xscale('log');ax.set(xlabel='Unique prefixes, IPv4 + IPv6',ylabel='Peak sampled Total−Available, MiB',title='Debian / FRR: sampled memory\nFull table at 2 GiB: bgpd OOM');ax.legend();ax.grid(alpha=.25);fig.savefig(img/'chart-R7-debian-memory.png',dpi=150);plt.close(fig)
a=json.loads((raw/'R6-debian-scale.json').read_text());fig,ax=plt.subplots(figsize=(9,6),layout='constrained')
for phase in ['full-replace','one-rule-edit','description-only']:
 rows=[r for r in a if r.get('phase')==phase and r.get('exit')==0]
 ax.plot([r['count'] for r in rows],[r['observer_wall_s'] for r in rows],'o-',label=phase)
ax.set_xscale('log');ax.set(xlabel='Firewall rule count',ylabel='SSH + native nft command, seconds',title='Debian nftables: observed wall time\nIncludes SSH; not separate kernel timing');ax.grid(alpha=.25);ax.legend(fontsize=13);fig.savefig(img/'chart-R6-debian-apply.png',dpi=150);plt.close(fig)
