#!/usr/bin/env python3
"""Read-only checks; no privileges, namespace policy or host links are changed."""
import datetime
import json
import os
import pathlib
import shutil
import sys

root = pathlib.Path(os.environ.get('FIELD_ROOT', '/work/field-edge'))
status = dict(line.split(':', 1) for line in pathlib.Path('/proc/self/status').read_text().splitlines() if ':' in line)
cap = int(status['CapEff'].strip(), 16)
checks = {
    'linux': sys.platform == 'linux',
    'kvm': os.access('/dev/kvm', os.R_OK | os.W_OK),
}
for name in ['qemu-system-x86_64', 'qemu-img', 'ip', 'tc', 'kubectl', 'asciinema', 'agg']:
    checks[name] = shutil.which(name) is not None
record = {
    'scenario_id': 'preflight', 'product': 'stand',
    'time_utc': datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'status': 'ready' if all(checks.values()) else 'blocked',
    'checks': checks, 'free_bytes': shutil.disk_usage('/work').free,
    'reason': ('Missing: ' + ', '.join(k for k, v in checks.items() if not v)) if not all(checks.values()) else 'Userspace QEMU fabric; no host privileges required',
    'network_method': 'socket multicast; gateway VM owns netem/NAT',
    'outage_seconds': None, 'acknowledged_record_loss': None,
    'recovery_seconds': None, 'manual_actions': [],
    'artifact_paths': ['02-edge/raw/phase2-preflight.txt'],
}
print(json.dumps(record, indent=2))
if '--record' in sys.argv:
    root.mkdir(parents=True, exist_ok=True)
    with (root / 'preflight.jsonl').open('a') as f:
        f.write(json.dumps(record) + '\n')
sys.exit(0 if all(checks.values()) else 2)
