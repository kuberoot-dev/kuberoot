#!/usr/bin/env python3
"""RTC trials with strict TLS and no WAN NTP. Preserve each mutated disk.
Needs the installed golden raw disk/NVRAM and an explicit CA-expiry timestamp.
Correction uses a power cycle with RTC=utc; all manual recovery stays explicit.
"""
import argparse,datetime,json,os,pathlib,subprocess,time
from qmp import command
p=argparse.ArgumentParser();p.add_argument('--golden',type=pathlib.Path,required=True);p.add_argument('--output',type=pathlib.Path,required=True);p.add_argument('--product',required=True);p.add_argument('--beyond-ca',required=True,help='UTC ISO timestamp strictly beyond the recorded CA notAfter');a=p.parse_args()
scripts=pathlib.Path(__file__).resolve().parent;a.output.mkdir(parents=True,exist_ok=False)
config=json.loads(subprocess.check_output(['kubectl','config','view','-o','json'],text=True))
assert all(c['cluster']['server'].startswith('https://') and not c['cluster'].get('insecure-skip-tls-verify') for c in config['clusters']), 'RTC tests require strict TLS'
now=datetime.datetime.now(datetime.timezone.utc)
cases=[('minus-24h',now-datetime.timedelta(days=1)),('plus-400d',now+datetime.timedelta(days=400)),('beyond-ca',datetime.datetime.fromisoformat(a.beyond_ca.replace('Z','+00:00')))]
subprocess.run([str(scripts/'wan-impairment.sh'),'blackhole'],check=True)
for name,rtc in cases:
 d=a.output/name;d.mkdir()
 for f in ['system.raw','vars.fd']:subprocess.run(['cp','--sparse=always',str(a.golden/f),str(d/f)],check=True)
 env=dict(os.environ,VM_DIR=str(d),RTC_BASE=rtc.strftime('%Y-%m-%dT%H:%M:%S'))
 subprocess.run(['bash','-c','source "$1/lib.sh"; vm_start 1','_',str(scripts)],env=env,check=True)
 rows=[]
 for i in range(12):
  r=subprocess.run(['kubectl','--request-timeout=3s','get','--raw','/readyz'],capture_output=True,text=True,timeout=5)
  row={'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'phase':'wrong-RTC','returncode':r.returncode,'stdout':r.stdout,'stderr':r.stderr};rows.append(row);print(json.dumps(row),flush=True);time.sleep(5)
 command(str(d/'qmp.sock'),'quit');time.sleep(2)
 (d/'wrong-rtc-serial.log').write_bytes((d/'serial.log').read_bytes())
 env['RTC_BASE']='utc';start=time.monotonic();recovered=None
 subprocess.run(['bash','-c','source "$1/lib.sh"; vm_start 1','_',str(scripts)],env=env,check=True)
 while time.monotonic()-start<300:
  r=subprocess.run(['kubectl','--request-timeout=3s','get','--raw','/readyz'],capture_output=True,text=True,timeout=5)
  row={'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'phase':'correct-RTC','returncode':r.returncode,'stdout':r.stdout,'stderr':r.stderr};rows.append(row)
  if r.returncode==0 and r.stdout.strip()=='ok':recovered=time.monotonic()-start;break
  time.sleep(2)
 command(str(d/'qmp.sock'),'quit');time.sleep(2)
 result={'scenario_id':'E6','product':a.product,'fault_phase':name,'rtc':rtc.isoformat(),'strict_tls':True,'api_recovery_seconds':recovered,'status':'api-recovered' if recovered is not None else 'manual-recovery-required','reason':'API-only checkpoint: application recovery and diagnosis must also be checked before assigning suite pass','manual_actions':['power cycle with RTC=utc'],'observations':rows}
 (d/'result.json').write_text(json.dumps(result,indent=2));print(json.dumps({k:v for k,v in result.items() if k!='observations'}),flush=True)
subprocess.run([str(scripts/'wan-impairment.sh'),'clean'],check=True)
