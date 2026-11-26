#!/usr/bin/env python3
"""Plot actual E2 client correctness and durable ACKs; missing data stays missing."""
import collections,datetime,gzip,json,math,pathlib,sys
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
p=pathlib.Path(sys.argv[1]);r=json.loads((p/'result.json').read_text())
source=p/'observer.jsonl'
if source.is_file():lines=source.read_text().splitlines()
elif source.with_suffix('.jsonl.gz').is_file():lines=gzip.open(source.with_suffix('.jsonl.gz'),'rt').read().splitlines()
else:raise SystemExit(0)
rows=[json.loads(l) for l in lines];rows=[x for x in rows if x.get('kind') in ['http','append','api'] and isinstance(x.get('start_seconds'),(int,float))]
if not rows:raise SystemExit(0)
start=min(datetime.datetime.fromisoformat(x['utc']).timestamp()-x['start_seconds'] for x in rows if x.get('utc'))
bins=collections.defaultdict(list);acks=[]
for x in rows:
 bins[(x['kind'],math.floor(x['start_seconds']/10))].append(x)
 if x['kind']=='append' and x.get('acknowledged'):acks.append(x['start_seconds']+x.get('latency_seconds',0))
plt.rcParams.update({'font.size':17});fig,axes=plt.subplots(2,1,figsize=(9,9),sharex=True,layout='constrained',gridspec_kw={'height_ratios':[1.5,1]})
for kind,label,color,style,width in [('http','HTTP body correct','#b83435','-',3.5),('append','Durable append ACK','#176da5','--',2.5),('api','ConfigMap API','#168042',':',2)]:
 indexes=sorted(i for k,i in bins if k==kind)
 if not indexes:continue
 values=[]
 for i in indexes:
  samples=bins[(kind,i)];values.append(100*sum(bool(x.get('ok')) and (kind!='append' or bool(x.get('acknowledged'))) for x in samples)/len(samples))
 axes[0].step([i*10+5 for i in indexes],values,where='mid',label=label,color=color,linestyle=style,linewidth=width)
axes[0].set(ylabel='Successful probes (%)',ylim=(-5,105));axes[0].legend(fontsize=12,loc='lower center',bbox_to_anchor=(.5,1.01),ncol=3,framealpha=.95)
acks.sort();times=sorted({0,*[math.ceil(t/10)*10 for t in acks],math.ceil(max(x['start_seconds'] for x in rows)/10)*10});count=0;counts=[]
for t in times:
 while count<len(acks) and acks[count]<=t:count+=1
 counts.append(count)
axes[1].step(times,counts,where='post',linewidth=2.5,color='#176da5');axes[1].set(ylabel='Cumulative ACKs (records)',xlabel='Seconds from observer start')
request=None;good=None
obs=p/'observations.jsonl'
if obs.is_file():
 for line in obs.read_text().splitlines():
  x=json.loads(line)
  if x.get('utc') and isinstance(x.get('elapsed_seconds'),(int,float)):
   if request is None:request=datetime.datetime.fromisoformat(x['utc']).timestamp()-x['elapsed_seconds']-start
   boot=x.get('booted',{})
   if not isinstance(boot,dict):boot={}
   if good is None and boot.get('release')==r.get('metrics',{}).get('target_version') and boot.get('state')=='Good':good=datetime.datetime.fromisoformat(x['utc']).timestamp()-start
if request is not None:
 for ax in axes:ax.axvline(request,color='black',linestyle='--',alpha=.6)
 axes[0].annotate('Upgrade request',xy=(request,100),xytext=(8,-16),textcoords='offset points',fontsize=12)
if good is not None:
 for ax in axes:ax.axvline(good,color='#9c620b',linestyle=':',alpha=.8)
 axes[0].annotate('B Good observed',xy=(good,52),xytext=(6,0),textcoords='offset points',fontsize=12)
rollback=r.get('metrics',{}).get('automatic_rollback_seconds')
if request is not None and rollback is not None:
 for ax in axes:ax.axvline(request+rollback,color='#8c4aab',linestyle='-.')
 axes[1].annotate('OS back on A',xy=(request+rollback,0),xytext=(-8,14),textcoords='offset points',ha='right',fontsize=13)
for ax in axes:ax.grid(alpha=.2)
loss=r.get('acknowledged_record_loss');loss_text='unknown' if loss is None else str(loss)
fig.suptitle(r['product']+'\n'+str(r.get('fault_phase'))+' / repeat '+str(r.get('repetition'))+' / '+r['status']+'\nACK loss: '+loss_text,fontsize=18)
fig.supxlabel('10 s bins; host I/O observer: '+('on' if r.get('metrics',{}).get('host_io_observation',{}).get('enabled') else 'off')+'\nUTC start '+datetime.datetime.fromtimestamp(start,datetime.timezone.utc).strftime('%Y-%m-%d %H:%M:%S'),fontsize=12)
fig.savefig(p/'service-curve.png',dpi=160);plt.close(fig)
(p/'service-curve-provenance.json').write_text(json.dumps({'source':str(source if source.exists() else source.with_suffix('.jsonl.gz')),'bin_seconds':10,'probe_counts':dict(collections.Counter(x['kind'] for x in rows)),'acknowledged_in_observer':len(acks),'loss_conclusion':loss,'request_marker_seconds':request,'utc_start':datetime.datetime.fromtimestamp(start,datetime.timezone.utc).isoformat()},indent=2))
