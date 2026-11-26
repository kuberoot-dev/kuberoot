#!/usr/bin/env python3
"""Pause only the queue parent after DUT shutdown, while its compressor runs.
Its measured child has already finished. Resume after verified block sharing.
"""
import os,pathlib,signal,subprocess,time
root=pathlib.Path('/work/field-edge');parent=int((root/'wan-gateway-v2.pid').read_text());target=str(root/'wan-gateway-v2-release/system.raw')
while True:
 os.kill(parent,0);compressors=[]
 for p in pathlib.Path('/proc').glob('[0-9]*/cmdline'):
  try:args=p.read_bytes().decode().split('\0')
  except (OSError,UnicodeError):continue
  if args[0].endswith('qemu-img') and len(args)>1 and args[1]=='convert' and target in args:compressors.append(int(p.parent.name))
 if compressors:break
 time.sleep(.5)
os.kill(parent,signal.SIGSTOP);print('Queue parent paused after measurement and ACPI shutdown',flush=True)
try:
 for pid in compressors:
  while pathlib.Path(f'/proc/{pid}/stat').exists():
   state=pathlib.Path(f'/proc/{pid}/stat').read_text().split()[2]
   if state=='Z':break
   time.sleep(1)
 subprocess.run(['python3',str(root/'scripts/deduplicate-forensic-disks.py')],check=True)
 subprocess.run(['df','-h','/work'],check=True)
finally:
 os.kill(parent,signal.SIGCONT);print('Queue resumed',flush=True)
