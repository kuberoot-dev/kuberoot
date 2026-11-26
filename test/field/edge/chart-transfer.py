#!/usr/bin/env python3
import json,pathlib,sys
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
d=pathlib.Path(sys.argv[1]);rows=[json.loads(x) for x in (d/'transfer-progress.jsonl').read_text().splitlines()];r=json.loads((d/'result.json').read_text())
plt.rcParams.update({'font.size':19});fig,axes=plt.subplots(2,1,figsize=(12,11),sharex=True,layout='constrained')
x=[v['elapsed_seconds']/60 for v in rows];axes[0].plot(x,[v.get('download_percent_quantized') if v.get('download_percent_quantized') is not None else float('nan') for v in rows],linewidth=2.5);axes[0].axhline(r['metrics']['requested_cut_percent'],linestyle='--',color='#b52a38',label='Requested cut threshold');axes[0].set_ylabel('Largest OCI layer, % (native CLI)' if r['product'].startswith('talos') else 'Downloaded, % (API)');axes[0].set_ylim(0,105);axes[0].legend()
axes[1].plot(x,[v['forwarded_download_ipv4_bytes']/2**20 for v in rows],linewidth=2.5);axes[1].set_ylabel('Gateway forwarded IPv4, MiB');axes[1].set_xlabel('Actual elapsed time, minutes')
for ax in axes:
 for left,right in zip(rows,rows[1:]):
  if not left['connected']:ax.axvspan(left['elapsed_seconds']/60,right['elapsed_seconds']/60,color='#aaaaaa',alpha=.12,linewidth=0)
 ax.grid(alpha=.2)
fig.suptitle(r['product']+' · real maintenance windows\nGray: WAN unavailable; 2 Mbit/s when connected',fontsize=21);fig.savefig(d/'transfer-curve.png',dpi=150)
