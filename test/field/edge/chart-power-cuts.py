#!/usr/bin/env python3
"""Plot every measured power-cut outcome; missing recovery is never drawn as zero."""
import json,pathlib,sys
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
current=pathlib.Path(sys.argv[1]);r=json.loads((current/'result.json').read_text())
rows=[json.loads(p.read_text()) for p in current.parent.glob('E3-*/result.json')];rows.sort(key=lambda x:x['repetition'])
plt.rcParams.update({'font.size':19});fig,ax=plt.subplots(figsize=(12,7),layout='constrained')
for row in rows:
 value=row.get('recovery_seconds');x=row['repetition']
 if value is not None:ax.scatter(x,value,s=100,c='#217346' if row['status'].startswith('passed') else '#bd3039')
 else:ax.scatter(x,0,s=130,c='#bd3039',marker='x');ax.annotate('not recovered',(x,0),xytext=(0,10),textcoords='offset points',rotation=90,fontsize=11)
ax.axhline(180,linestyle='--',color='#bd3039',label='180 s target');ax.set(xlabel='Seeded power cut (repetition)',ylabel='Recovery to 60 s sustained W success, s',title=r['product']+' · power-cut recovery',xlim=(.3,30.7),ylim=(-15,max(210,*[x.get('recovery_seconds') or 0 for x in rows])*1.15));ax.grid(alpha=.2);ax.legend();fig.savefig(current/'recovery-curve.png',dpi=150)
