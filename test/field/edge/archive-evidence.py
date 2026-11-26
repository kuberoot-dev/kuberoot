#!/usr/bin/env python3
"""Losslessly compress bulky samples, never terminal .txt transcripts.
Verify the decompressed SHA256 before unlinking a completed source file.
"""
import gzip,hashlib,json,pathlib,shutil

def archive(path):
 path=pathlib.Path(path);assert path.is_relative_to(pathlib.Path('/work/field-edge'))
 if path.suffix!='.jsonl' and path.name not in ['records','recovered-records'] and not path.name.endswith('-records'):return path
 target=path.with_name(path.name+'.gz');proof=target.with_name(target.name+'.integrity.json')
 if not path.exists() and target.exists():
  with gzip.open(target,'rb') as f:actual=hashlib.file_digest(f,'sha256').hexdigest()
  assert actual==json.loads(proof.read_text())['original_sha256'];return target
 if target.exists():
  expected=hashlib.file_digest(path.open('rb'),'sha256').hexdigest()
  with gzip.open(target,'rb') as f:actual=hashlib.file_digest(f,'sha256').hexdigest()
  assert expected==actual==json.loads(proof.read_text())['original_sha256'];path.unlink();return target
 if path.stat().st_size<256*1024:return path
 temporary=target.with_name(target.name+'.partial')
 if temporary.exists():temporary.unlink()
 expected=hashlib.file_digest(path.open('rb'),'sha256').hexdigest()
 with path.open('rb') as src,gzip.open(temporary,'wb',compresslevel=3) as out:shutil.copyfileobj(src,out,1024**2)
 with gzip.open(temporary,'rb') as f:actual=hashlib.file_digest(f,'sha256').hexdigest()
 assert expected==actual
 proof.write_text(json.dumps({'original_sha256':expected,'original_bytes':path.stat().st_size,'decompressed_comparison_equal':True},indent=2))
 temporary.replace(target);path.unlink();return target

def archive_transfers(transfers):
 result={}
 for destination,source in transfers.items():
  archived=archive(source)
  result[destination+('.gz' if str(archived)!=source else '')]=str(archived)
  if str(archived)!=source:result[destination+'.gz.integrity.json']=str(archived)+'.integrity.json'
 return result
