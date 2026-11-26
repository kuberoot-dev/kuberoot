#!/usr/bin/env python3
"""Compare the outside observer's acknowledged IDs to recovered workload bytes."""
import collections
import json
import pathlib
import sys

acks = {r['id'] for line in pathlib.Path(sys.argv[1]).read_text().splitlines()
        if (r := json.loads(line)).get('kind') == 'append' and r.get('acknowledged')}
data = pathlib.Path(sys.argv[2]).read_bytes()
records = [data[i:i+1024] for i in range(0, len(data), 1024)]
corrupt = sum(len(r) != 1024 or not r[:20].isdigit() or r[20:] != b'W' * 1004 for r in records)
ids = [int(r[:20]) for r in records if len(r) == 1024 and r[:20].isdigit()]
missing = sorted(acks - set(ids))
print(json.dumps({'acknowledged': len(acks), 'recovered': len(ids),
                  'acknowledged_record_loss': len(missing), 'missing_ids': missing,
                  'corrupt_records': corrupt,
                  'duplicate_records': sum(n - 1 for n in collections.Counter(ids).values())}))
sys.exit(1 if missing or corrupt else 0)
