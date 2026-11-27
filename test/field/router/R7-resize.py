#!/usr/bin/env python3
import argparse,json,os,subprocess,time
from pathlib import Path
p=argparse.ArgumentParser();p.add_argument('gib',choices=['4','8']);p.add_argument('--resize-generator',action='store_true');a=p.parse_args()
def guest(vm,cmd):return subprocess.run(['/work/field/guest-command',vm,cmd],capture_output=True,text=True)
def stop(vm):
 subprocess.run(['python3','/work/field/qmp.py',vm,'quit'],check=True);time.sleep(2);Path('/work/vms/'+vm+'/pid').unlink(missing_ok=True)
def wait(vm):
 for _ in range(90):
  if guest(vm,'true').returncode==0:return
  time.sleep(2)
 raise SystemExit(vm+' did not boot')
stop('debian')
if a.resize_generator:
 stop('gateway');subprocess.run(['python3','/work/field/fabric-vm.py','gateway'],env=dict(os.environ,ENDPOINT_RAM_MIB='2048',ENDPOINT_VCPU='2'),check=True);wait('gateway');subprocess.run(['bash','/work/field/fabric-configure.sh','gateway'],check=True)
else:
 for side in [1,2]:guest('gateway',f'kill $(cat /run/field-bird/{side}.pid)')
subprocess.run(['bash','/work/field/fabric-competitor.sh','debian'],env=dict(os.environ,DUT_RAM_MIB=str(int(a.gib)*1024),DUT_VCPU='4'),check=True);wait('debian')
print(time.strftime('%FT%TZ',time.gmtime()),'DUT',a.gib,'GiB / 4 vCPU; generator 2 GiB / 2 vCPU',flush=True)
subprocess.run(['python3','/work/field/R7-debian.py'],env=dict(os.environ,FIELD_PREFIX_COUNTS='800000',FIELD_IPV6_COUNT='200000',FIELD_DUT_RAM_GIB=a.gib),check=True)
