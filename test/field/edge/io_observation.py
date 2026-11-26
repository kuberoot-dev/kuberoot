"""Declare metadata-only host I/O observation, identically for E2 products."""
import hashlib,json,pathlib
ROOT=pathlib.Path('/work/field-edge')
def enable(directory,env,metrics):
 library=ROOT/'io-observation/qemu-io-observation.so';source=ROOT/'scripts/qemu-io-diagnostic.c'
 assert library.is_file() and source.is_file(),'Build metadata-only I/O observer before E2'
 record={'enabled':True,'scope':'QEMU pread64/pwrite64/pwritev64 metadata only; arguments, data and return values unchanged','library_sha256':hashlib.sha256(library.read_bytes()).hexdigest(),'source_sha256':hashlib.sha256(source.read_bytes()).hexdigest(),'baseline_variant':'earlier E2 runs without this field had no host-call observer; actual wall-clock durations retained without correction'}
 expected={line.split()[1]:line.split()[0] for line in (ROOT/'io-observation/build.sha256').read_text().splitlines()}
 assert expected[str(library)]==record['library_sha256'] and expected[str(source)]==record['source_sha256'],'I/O observer source/binary provenance mismatch'
 env['FIELD_IO_TRACE']='1';metrics['host_io_observation']=record
 (directory/'io-observation-provenance.json').write_text(json.dumps(record,indent=2))
