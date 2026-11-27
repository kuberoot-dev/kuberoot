#!/usr/bin/env python3
"""Wait for the measured direct ceiling before consuming build CPU."""
from pathlib import Path
import subprocess,time
while True:
 active=False
 for p in Path('/proc').glob('[0-9]*/cmdline'):
  try:
   cmd=p.read_bytes().split(b'\0')
   if b'/work/field/forwarding.py' in cmd:active=True
  except OSError:pass
 if not active:break
 time.sleep(5)
with open('/work/field/raw/field-fixes-tests.txt','w') as f:
 p=subprocess.run(['bash','/work/field/test-field-fixes.sh'],stdout=f,stderr=subprocess.STDOUT)
Path('/work/field/raw/field-fixes-tests.exit').write_text(str(p.returncode))
if p.returncode:raise SystemExit(p.returncode)
with open('/work/field/raw/field-fixes-build.txt','w') as f:
 p=subprocess.run(['bash','/work/field/build-field-fixes.sh'],stdout=f,stderr=subprocess.STDOUT)
Path('/work/field/raw/field-fixes-build.exit').write_text(str(p.returncode))
