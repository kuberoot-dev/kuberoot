#!/usr/bin/env python3
"""Plot real client observations around one storage injection and relief."""
import datetime,json,math,pathlib,sys
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt

d=pathlib.Path(sys.argv[1]);result=json.loads((d/'result.json').read_text())
if not (d/'observer.jsonl').exists() or not (d/'fault-epoch').exists():
 print('No completed storage time series; no chart synthesized.');raise SystemExit(0)
fault=float((d/'fault-epoch').read_text());relief=float((d/'relief-epoch').read_text()) if (d/'relief-epoch').exists() else None
bins={}
for line in (d/'observer.jsonl').read_text().splitlines():
 r=json.loads(line)
 if not r.get('utc'):continue
 minute=math.floor((datetime.datetime.fromisoformat(r['utc']).timestamp()-fault)/60)
 bins.setdefault(minute,[]).append(r)
if not bins:raise SystemExit('No timed observations in storage trial')
x=[];failures=[];latency=[];counts=[]
for minute,rows in sorted(bins.items()):
 requests=[r for r in rows if r['kind'] in ['http','append']]
 api=sorted(o['latency_seconds'] for r in rows if r['kind']=='api' for o in r.get('api_operations',[]))
 x.append(minute+.5);counts.append(len(requests))
 failures.append(100*sum(not r.get('ok') for r in requests)/len(requests) if requests else math.nan)
 latency.append(api[max(0,math.ceil(.99*len(api))-1)] if api else math.nan)
plt.rcParams.update({'font.size':20})
fig,axes=plt.subplots(3,1,figsize=(11,11),sharex=True,layout='constrained')
axes[0].plot(x,failures,'o-',color='#bd3039');axes[0].set_ylabel('W failures, %')
axes[1].plot(x,latency,'o-',color='#2166ac');axes[1].set_ylabel('API op p99, s')
axes[2].bar(x,counts,width=.8,color='#777777');axes[2].set_ylabel('W requests / min');axes[2].set_xlabel('Minutes from storage fault (UTC clock)')
for ax in axes:
 ax.axvline(0,color='#bd3039',linestyle='--',label='Fault');ax.grid(alpha=.2)
 if relief is not None:ax.axvline((relief-fault)/60,color='#217346',linestyle='--',label='Relief')
 ax.set_ylim(bottom=0)
axes[0].legend(loc='upper right',fontsize=16)
fig.suptitle(result['product']+' · '+result['fault_phase']+'\n'+result['status'],fontsize=20)
fig.savefig(d/'storage-curve.png',dpi=150)
