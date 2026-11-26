#!/usr/bin/env python3
import json,os,subprocess,time
from pathlib import Path
raw=Path('/work/field/raw')
while not (raw/'R9-debian.exit').exists():time.sleep(3)
print('Resize only upstream generator to 4096 MiB; DUT remains 2 vCPU / 2 GiB',flush=True)
subprocess.run(['python3','/work/field/qmp.py','gateway','quit'],check=True);time.sleep(2);Path('/work/vms/gateway/pid').unlink(missing_ok=True)
subprocess.run(['python3','/work/field/fabric-vm.py','gateway'],env=dict(os.environ,ENDPOINT_RAM_MIB='4096',ENDPOINT_VCPU='2'),check=True)
for _ in range(60):
 p=subprocess.run(['/work/field/guest-command','gateway','true'],capture_output=True)
 if p.returncode==0:break
 time.sleep(2)
else:raise SystemExit('generator reboot did not recover')
subprocess.run(['bash','/work/field/fabric-configure.sh','gateway'],check=True)
subprocess.run(['python3','/work/field/R7-debian.py'],env=dict(os.environ,FIELD_PREFIX_COUNTS='800000',FIELD_IPV6_COUNT='200000'),check=True)
