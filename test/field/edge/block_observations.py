"""Observe unexpected QEMU backend errors in E2, which injects no disk faults."""
import json
from qmp import command

def collect(directory,metrics,errors):
 try:
  counters=command(str(directory/'qmp.sock'),'query-blockstats')
  (directory/'block-counters.json').write_text(json.dumps(counters,indent=2))
  metrics['unexpected_block_io_failures']=sum(int(x.get('stats',{}).get(k,0)) for x in counters for k in ['failed_wr_operations','failed_rd_operations','failed_flush_operations'])
  if metrics['unexpected_block_io_failures']:errors.append('unexpected QEMU backend I/O errors; trial invalid')
 except Exception as exc:metrics['block_counters_error']=str(exc)
