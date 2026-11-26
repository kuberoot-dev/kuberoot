#!/usr/bin/env python3
"""Append one measured run atomically enough for interruption recovery.

stdin: JSON result. argv[1]: output JSONL. Never derives a verdict from exit code.
"""
import datetime,fcntl,json,os,sys
from pathlib import Path
fields='test_id product image_sha256 package_versions profile topology vcpu ram queues offloads license workload_seed fault_seed input_revision applied_revision timeout result unsupported_reason recovery_s service_gap_s lost_packets tcp_resets cpu_core_seconds peak_memory_bytes disk_write_bytes manual_actions artifact_paths'.split()
row=json.load(sys.stdin)
for required in ('test_id','product','result','artifact_paths'):
 if required not in row:raise SystemExit(f'missing {required}')
if row['result'] not in ('pass','fail','measured','partial','unsupported','blocked','not_measured'):raise SystemExit('invalid result')
if row['result'] in ('blocked','unsupported','not_measured','partial') and not row.get('reason'):raise SystemExit('reason required')
if not isinstance(row['artifact_paths'],list):raise SystemExit('artifact_paths must be a list')
row={**dict.fromkeys(fields),**row,'recorded_at':datetime.datetime.now(datetime.timezone.utc).isoformat()}
p=Path(sys.argv[1]);p.parent.mkdir(parents=True,exist_ok=True)
with p.open('a') as f:
 fcntl.flock(f,fcntl.LOCK_EX);f.write(json.dumps(row,sort_keys=True)+'\n');f.flush();os.fsync(f.fileno())
