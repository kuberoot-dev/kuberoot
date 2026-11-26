#!/bin/bash
# Validate observer accounting and torn-tail recovery inside the workshop pod.
set -euo pipefail
cd "$(dirname "$0")"
test "$(uname -s)" = Linux
d=$(mktemp -d /work/tmp/edge-observer.XXXXXX)
python3 workload.py --port 18081 --records "$d/records" >"$d/server.log" 2>&1 &
server=$!
trap 'kill "$server" 2>/dev/null || true' EXIT
for attempt in $(seq 1 30); do
  if curl -fsS http://127.0.0.1:18081/http -o /dev/null; then break; fi
  sleep .1
done
python3 observer.py --http http://127.0.0.1:18081 --append http://127.0.0.1:18081 --seconds 5 --output "$d/observer.jsonl"
python3 check-records.py "$d/observer.jsonl" "$d/records"
python3 - "$d" <<'PY'
import json,pathlib,sys
p=pathlib.Path(sys.argv[1]); rows=[json.loads(s) for s in (p/'observer.jsonl').read_text().splitlines()]
assert len(rows)==150, len(rows)
assert sum(r['kind']=='append' for r in rows)==50
assert all(r['ok'] for r in rows)
print('PASS: 100 HTTP responses, 50 fsync-acknowledged appends; every response checked')
PY
kill "$server"
wait "$server" || true
python3 - "$d/records" <<'PY'
import pathlib,sys
from workload import recover
p=pathlib.Path(sys.argv[1]); original=p.read_bytes()
with p.open('ab') as f: f.write(b'partial unacknowledged write')
recover(p)
assert p.read_bytes()==original
print('PASS: unacknowledged torn tail removed, acknowledged bytes unchanged')
p.write_bytes(original[1024:])
PY
if python3 check-records.py "$d/observer.jsonl" "$d/records"; then
  echo 'FAIL: missing acknowledged record was not detected'; exit 1
fi
printf 'PASS: deliberate acknowledged-record loss detected; evidence: %s\n' "$d"
