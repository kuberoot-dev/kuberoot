#!/usr/bin/env python3
"""Read GPT boot partitions from an owned raw image; never mount or modify it.
Call with the VM stopped/paused. State partitions are deliberately excluded.
"""
import argparse,hashlib,json,pathlib,struct,uuid
p=argparse.ArgumentParser();p.add_argument('disk',type=pathlib.Path);a=p.parse_args()
assert a.disk.is_relative_to(pathlib.Path('/work/field-edge'))
with a.disk.open('rb') as f:
 f.seek(512);header=f.read(512);assert header[:8]==b'EFI PART'
 lba,count,size=struct.unpack_from('<QII',header,72);assert 0<count<=1024 and 128<=size<=4096
 f.seek(lba*512);entries=f.read(count*size);result=[]
 for i in range(count):
  e=entries[i*size:(i+1)*size]
  if e[:16]==bytes(16):continue
  label=e[56:128].decode('utf-16-le').rstrip('\0');kind=str(uuid.UUID(bytes_le=e[:16]))
  if kind!='c12a7328-f81f-11d2-ba4b-00a0c93ec93b' and label.upper() not in ['EFI','BOOT'] and not label.startswith('kuberoot-root-'):continue
  start,end=struct.unpack_from('<QQ',e,32);length=(end-start+1)*512
  assert 0<length<=4*1024**3
  f.seek(start*512);h=hashlib.sha256();left=length
  while left:
   b=f.read(min(left,1024**2));assert b;h.update(b);left-=len(b)
  result.append({'partition':i+1,'label':label,'bytes':length,'sha256':h.hexdigest()})
 assert result,'No boot partitions identified'
 print(json.dumps(result,indent=2))
