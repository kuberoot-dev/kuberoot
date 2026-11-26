#!/usr/bin/env python3
"""Stream the real QEMU serial file, retaining a real UTC clock in the capture."""
import argparse,pathlib,subprocess,sys,time
p=argparse.ArgumentParser();p.add_argument('serial',type=pathlib.Path);p.add_argument('--stop-file',type=pathlib.Path,required=True);p.add_argument('--max-seconds',type=float,default=600);p.add_argument('--from-end',action='store_true');a=p.parse_args()
def clock():
 print('\n$ ',end='',flush=True)
 for ch in 'date -u':print(ch,end='',flush=True);time.sleep(.012)
 print(flush=True);subprocess.run(['date','-u'],check=True)
clock();start=time.monotonic();next_clock=start+30;offset=a.serial.stat().st_size if a.from_end and a.serial.exists() else 0
while time.monotonic()-start<a.max_seconds and not a.stop_file.exists():
 if a.serial.exists():
  size=a.serial.stat().st_size
  if size<offset:offset=0
  if size>offset:
   with a.serial.open('rb') as f:f.seek(offset);data=f.read();offset=f.tell()
   sys.stdout.buffer.write(data);sys.stdout.buffer.flush()
 if time.monotonic()>=next_clock:clock();next_clock=time.monotonic()+30
 time.sleep(.1)
clock()
