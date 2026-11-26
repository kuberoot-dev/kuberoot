#!/usr/bin/env python3
"""Plot actual cold-boot milestone samples; failed/missing milestones stay missing."""
import json,pathlib,sys
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
root=pathlib.Path('/work/field-edge')
rows=[json.loads(x) for x in (root/'results.jsonl').read_text().splitlines()]
if len(sys.argv)>1:rows.append(json.loads(pathlib.Path(sys.argv[1]).read_text()))
rows=[r for r in rows if r.get('scenario_id')=='E-B2' and r.get('measurement_protocol')=='boot-readiness-v2']
products=['kuberoot-v0.1.0','kuberoot-main-1b29543','k3s-v1.37.2-k3s1','talos-v1.14.7']
if any(len({r['repetition'] for r in rows if r['product']==p})<10 for p in products):raise SystemExit(3)
plt.rcParams.update({'font.size':18})
fig,axes=plt.subplots(4,1,figsize=(11,19),layout='constrained')
labels=['kuberoot\nrelease','kuberoot\nmain','k3s +\nUbuntu','Talos']
for ax,key,title in zip(axes,['api_ready_seconds','node_ready_fresh_heartbeat_seconds','coredns_answer_seconds','new_pod_serves_seconds'],['API /readyz','Fresh Node Ready heartbeat','CoreDNS answer','New pod serves HTTP']):
 for i,product in enumerate(products):
  measured=[r['metrics'][key] for r in rows if r['product']==product and key in r.get('metrics',{})]
  if measured:
   ax.boxplot([measured],positions=[i],widths=.5,showfliers=False)
   ax.scatter([i+(j-(len(measured)-1)/2)*.025 for j in range(len(measured))],measured,s=45,alpha=.65)
  ax.text(i,.97,f'n={len(measured)}',transform=ax.get_xaxis_transform(),ha='center',va='top',fontsize=14)
 ax.set_xticks(range(4),labels);ax.set_ylabel('Seconds from power-on');ax.set_title(title);ax.set_ylim(0,ax.get_ylim()[1]*1.18);ax.grid(axis='y',alpha=.2)
fig.suptitle('Installed cold boot · 2 vCPU / 4 GiB\n10 fresh clones / product · cached image',fontsize=23)
fig.savefig(root/'captures/chart-cold-boots.png',dpi=150)
