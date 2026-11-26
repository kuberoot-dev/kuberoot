#!/usr/bin/env python3
"""Plot measured W errors and latency during the real one-hour WAN outage."""
import collections,datetime,json,math,os,pathlib,sys
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
root=pathlib.Path('/work/field-edge');directories=[pathlib.Path(x) for x in sys.argv[1:]]
plt.rcParams.update({'font.size':18})
for d in directories:
 record=json.loads((d/'result.json').read_text());fault=float((d/'fault-epoch').read_text());cut=float((d/'power-cut-epoch').read_text());bins=collections.defaultdict(list)
 for line in (d/'observer.jsonl').read_text().splitlines():
  r=json.loads(line)
  if r['kind'] not in ['http','append'] or 'utc' not in r:continue
  minute=math.floor((datetime.datetime.fromisoformat(r['utc']).timestamp()-fault)/60)
  bins[minute].append(r)
 x=sorted(bins);failed=[sum(not r.get('ok') for r in bins[m]) for m in x]
 p99=[]
 for m in x:
  samples=sorted(r['latency_seconds']*1000 for r in bins[m] if 'latency_seconds' in r)
  p99.append(samples[math.ceil(.99*len(samples))-1] if samples else float('nan'))
 fig,axes=plt.subplots(2,1,figsize=(11,10),sharex=True,layout='constrained')
 axes[0].bar(x,failed,width=.85,color='#b64b4b');axes[0].set_yscale('symlog',linthresh=1);axes[0].set_ylabel('Failed requests / minute (symlog)')
 axes[1].plot(x,p99,'o-',markersize=3);axes[1].set_yscale('log');axes[1].set_ylabel('Client p99 latency (ms)');axes[1].set_xlabel('Minutes from WAN blackhole')
 for ax in axes:
  ax.axvspan(0,60,color='gray',alpha=.1,label='WAN blackhole')
  ax.axvline((cut-fault)/60,color='black',linestyle='--',label='Power cut');ax.grid(axis='y',alpha=.2)
 axes[0].legend(fontsize=14)
 fig.suptitle(record['product']+'\nW: 20 HTTP + 10 fsync appends/s',fontsize=21)
 fig.savefig(d/'wan-curve.png',dpi=150);plt.close(fig)
all_cases=[root/'results'/('E1-'+os.environ.get('WAN_RUN_LABEL','gateway-v2')+'-'+p) for p in ['kuberoot-v0.1.0','kuberoot-main-1b29543','k3s-v1.37.2-k3s1','talos-v1.14.7']]
if all((d/'result.json').exists() for d in all_cases):
 rows=[json.loads((d/'result.json').read_text()) for d in all_cases]
 fig,ax=plt.subplots(figsize=(11,7),layout='constrained');labels=['kuberoot\nrelease','kuberoot\nmain','k3s +\nUbuntu','Talos']
 for i,r in enumerate(rows):
  value=r.get('recovery_seconds')
  if value is None:ax.text(i,10,'Not recovered',ha='center',rotation=90);continue
  ax.bar(i,value,color='#3b788c');ax.text(i,value+4,f'{value:.1f} s',ha='center')
 ax.set_xticks(range(4),labels);ax.set_ylabel('Recovery after power cut (s)');ax.axhline(180,color='red',linestyle='--',label='180 s threshold');ax.legend();ax.set_ylim(bottom=0);ax.grid(axis='y',alpha=.2)
 ax.set_title('E1: first 60 s of sustained W success\nOne observation per product; continuity errors reported separately')
 fig.savefig(root/'captures'/('chart-wan-'+os.environ.get('WAN_RUN_LABEL','gateway-v2')+'-recovery.png'),dpi=150)
