#!/usr/bin/env python3
"""Pause a named owned queue only after its measured DUT stopped, then maintain."""
import argparse,os,pathlib,signal,subprocess,time
p=argparse.ArgumentParser();p.add_argument('--parent',type=int,required=True);p.add_argument('--disk',type=pathlib.Path,required=True);p.add_argument('--maintenance',type=pathlib.Path,required=True);a=p.parse_args()
root=pathlib.Path('/work/field-edge');assert a.disk.is_relative_to(root) and a.maintenance.is_relative_to(root/'scripts')
while True:
 os.kill(a.parent,0);children=[]
 for path in pathlib.Path('/proc').glob('[0-9]*/cmdline'):
  try:argv=path.read_bytes().decode().split('\0')
  except (OSError,UnicodeError):continue
  if argv[0].endswith('qemu-img') and len(argv)>1 and argv[1]=='convert' and str(a.disk) in argv:children.append(int(path.parent.name))
 if children:break
 time.sleep(.5)
os.kill(a.parent,signal.SIGSTOP);print('Queue paused at stopped-DUT compression boundary',flush=True)
for pid in children:
 while pathlib.Path(f'/proc/{pid}/stat').exists():
  try:state=pathlib.Path(f'/proc/{pid}/stat').read_text().split()[2]
  except FileNotFoundError:break
  if state=='Z':break
  time.sleep(1)
# On error the parent stays stopped; preserve evidence and repair before resuming.
subprocess.run(['python3',str(a.maintenance)],check=True)
subprocess.run(['df','-h','/work'],check=True)
os.kill(a.parent,signal.SIGCONT);print('Maintenance verified, queue resumed',flush=True)
