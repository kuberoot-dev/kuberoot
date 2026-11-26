#!/usr/bin/env python3
"""Plot only the four completed idle runs, with scope labels on storage metrics."""
import json,pathlib,sys
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
root=pathlib.Path('/work/field-edge');keys=['kuberoot-release','kuberoot-main-1b29543','k3s-v1.37.2-k3s1','talos-v1.14.7'];labels=['kuberoot\nrelease','kuberoot\nmain','k3s +\nUbuntu','Talos']
rows=[]
for key in keys:
 d=root/'results'/('E-B1-'+key);r=json.loads((d/'result.json').read_text());assert r['status']=='measured' and r['samples_ok']==61
 r['footprint']=json.loads((d/'footprint.json').read_text());rows.append(r)
plt.rcParams.update({'font.size':20});fig,axes=plt.subplots(4,1,figsize=(9,18));colors=['#2878b5','#4fa3d1','#e5a23c','#59a267']
series=[('Guest working set (MiB)',[r['metrics']['guest_memory_workingSetBytes_median']/2**20 for r in rows]),('Idle CPU (cores)',[r['metrics']['cpu_cores'] for r in rows]),('Whole raw disk: host allocation (GiB)',[r['footprint']['raw_disk_host_allocated_bytes']/2**30 for r in rows]),('Virtual writes (MiB/hour)',[r['metrics']['virtual_block_write_bytes_per_hour']/2**20 for r in rows])]
for ax,(title,values) in zip(axes,series):
 bars=ax.barh(labels,values,color=colors);ax.invert_yaxis();ax.set_xlabel(title);ax.grid(axis='x',alpha=.25);ax.set_axisbelow(True);ax.set_xlim(0,max(values)*1.24)
 for bar,value in zip(bars,values):ax.text(value+max(values)*.015,bar.get_y()+bar.get_height()/2,f'{value:.3f}' if max(values)<1 else f'{value:.1f}',va='center',fontsize=20)
fig.suptitle('30 min idle · 2 vCPU / 4 GiB\nProduct defaults, differing Kubernetes versions',fontsize=22)
fig.tight_layout(rect=(0,0.02,1,.96));fig.text(.5,.005,'Raw allocation ≠ guest filesystem used. Virtual writes ≠ NAND wear.',ha='center',fontsize=14)
out=root/'captures/chart-idle-comparison.png';fig.savefig(out,dpi=130);print(out)
