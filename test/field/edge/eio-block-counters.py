import json,sys
sys.path.insert(0,'/work/field-edge/scripts')
from qmp import command
r=command('/work/field-edge/results/E2-v2-kuberoot-main-1b29543-init-crash-2/qmp.sock','query-blockstats')
for b in r:
 if b.get('device')=='virtio0':
  print(json.dumps({'device':b['device'],**{k:b['stats'][k] for k in ['failed_wr_operations','failed_rd_operations','failed_flush_operations','wr_bytes']}},indent=2))
