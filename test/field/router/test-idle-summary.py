#!/usr/bin/env python3
"""Regression tests for time quantization; synthetic fixtures, not field results."""
import copy,json,subprocess,sys,tempfile
from pathlib import Path
base={'elapsed_s':0,'osconfigs':{'items':[{'status':{'bootTime':'2026-01-01T00:00:00Z','memoryTotal':1024,'memoryFree':512}}]},'nodeservices':{'items':[{'metadata':{'name':'router'},'status':{'pid':100,'startedAt':'2026-01-01T00:00:02Z','restarts':0}}]},'blockstats':[{'device':'disk','stats':{'wr_bytes':100}}]}
a=copy.deepcopy(base);b=copy.deepcopy(base);b['elapsed_s']=3600;b['blockstats'][0]['stats']['wr_bytes']=200;b['osconfigs']['items'][0]['status']['bootTime']='2026-01-01T00:00:01Z'
cases=[('one-second-rounding',[a,b],True)]
c=copy.deepcopy(b);c['osconfigs']['items'][0]['status']['bootTime']='2026-01-01T00:00:30Z';cases.append(('boot-changed',[a,c],False))
c=copy.deepcopy(b);c['nodeservices']['items'][0]['status']['pid']=101;cases.append(('service-restarted',[a,c],False))
c=copy.deepcopy(b);c['blockstats'][0]['stats']['wr_bytes']=50;cases.append(('counter-reset',[a,c],False))
with tempfile.TemporaryDirectory(prefix='router-idle-unit-') as d:
 for name,rows,ok in cases:
  p=Path(d)/(name+'.jsonl');p.write_text(''.join(json.dumps(r)+'\n' for r in rows))
  r=subprocess.run([sys.executable,str(Path(__file__).with_name('summarize-idle.py')),str(p)],capture_output=True,text=True)
  assert (r.returncode==0)==ok,(name,r.stderr)
  print(name,'PASS')
