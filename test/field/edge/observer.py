#!/usr/bin/env python3
"""Outside-DUT fixed-rate HTTP/append observer, persistent per-request evidence.

A scheduled request that cannot be dispatched is recorded as driver_overload;
this invalidates the load lane rather than silently reducing its rate.
"""
import argparse
import concurrent.futures
import datetime
import json
import os
import pathlib
import threading
import time
import urllib.request


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--http', required=True)
    ap.add_argument('--append', required=True)
    ap.add_argument('--seconds', type=float, required=True)
    ap.add_argument('--output', type=pathlib.Path, required=True)
    args = ap.parse_args()
    # Exclusive create prevents accidentally overwriting an acknowledgement log.
    out = args.output.open('x')
    lock = threading.Lock()
    slots = threading.BoundedSemaphore(100)
    origin = time.monotonic()

    def log(record):
        with lock:
            out.write(json.dumps(record) + '\n')
            out.flush()
            os.fsync(out.fileno())

    def probe(kind, number, scheduled):
        start = time.monotonic()
        record = {'kind': kind, 'id': number, 'scheduled_seconds': scheduled,
                  'start_seconds': start - origin,
                  'utc': datetime.datetime.now(datetime.timezone.utc).isoformat()}
        try:
            data = f'{number:020d}'.encode() + b'W' * 1004 if kind == 'append' else None
            url = args.append + '/append' if data else args.http + '/http'
            with urllib.request.urlopen(urllib.request.Request(url, data=data), timeout=2) as response:
                body = response.read()
                expected = data[:20] if data else b'W' * 1024
                record['ok'] = response.status == 200 and body == expected
                record['status'] = response.status
                record['acknowledged'] = kind == 'append' and record['ok']
                if not record['ok']:
                    record['error'] = 'unexpected response'
        except Exception as exc:
            record.update(ok=False, acknowledged=False, error=str(exc))
        finally:
            record['latency_seconds'] = time.monotonic() - start
            log(record)
            slots.release()

    with concurrent.futures.ThreadPoolExecutor(max_workers=100) as pool:
        tick = 0
        while tick / 20 < args.seconds:
            scheduled = tick / 20
            time.sleep(max(0, origin + scheduled - time.monotonic()))
            for kind, number in [('http', tick)] + ([('append', tick // 2)] if tick % 2 == 0 else []):
                if slots.acquire(blocking=False):
                    pool.submit(probe, kind, number, scheduled)
                else:
                    log({'kind': kind, 'id': number, 'ok': False, 'acknowledged': False,
                         'scheduled_seconds': scheduled, 'error': 'driver_overload'})
            tick += 1
    out.close()


if __name__ == '__main__':
    main()
