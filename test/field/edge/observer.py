#!/usr/bin/env python3
"""Outside-DUT fixed-rate HTTP/append observer, persistent per-request evidence.

A scheduled request that cannot be dispatched is recorded as driver_overload;
this invalidates the load lane rather than silently reducing its rate.
"""
import argparse
import base64
import concurrent.futures
import datetime
import json
import os
import pathlib
import ssl
import tempfile
import threading
import time
import urllib.request


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--http', required=True)
    ap.add_argument('--append', required=True)
    ap.add_argument('--seconds', type=float, required=True)
    ap.add_argument('--output', type=pathlib.Path, required=True)
    ap.add_argument('--stop-file', type=pathlib.Path)
    ap.add_argument('--id-base', type=int, default=0, help='Unique record range for consecutive observers on one disk')
    ap.add_argument('--kubeconfig', type=pathlib.Path, help='Enable 0.2 Hz ConfigMap create/read/delete probes')
    args = ap.parse_args()
    # Exclusive create prevents accidentally overwriting an acknowledgement log.
    out = args.output.open('x')
    lock = threading.Lock()
    slots = threading.BoundedSemaphore(100)
    origin = time.monotonic()

    api = None
    credentials_dir = None
    if args.kubeconfig:
        config = json.loads(args.kubeconfig.read_text())
        context = next(c['context'] for c in config['contexts'] if c['name'] == config['current-context'])
        cluster = next(c['cluster'] for c in config['clusters'] if c['name'] == context['cluster'])
        user = next(u['user'] for u in config['users'] if u['name'] == context['user'])
        assert cluster['server'].startswith('https://') and not cluster.get('insecure-skip-tls-verify')
        tls = ssl.create_default_context(cadata=base64.b64decode(cluster['certificate-authority-data']).decode())
        credentials_dir = tempfile.TemporaryDirectory(prefix='edge-observer-tls-')
        cert = pathlib.Path(credentials_dir.name) / 'cert.pem'
        key = pathlib.Path(credentials_dir.name) / 'key.pem'
        for path, field in [(cert, 'client-certificate-data'), (key, 'client-key-data')]:
            path.write_bytes(base64.b64decode(user[field])); path.chmod(0o600)
        tls.load_cert_chain(cert, key)
        api = (cluster['server'].rstrip('/') + '/api/v1/namespaces/field/configmaps', tls)

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
            if kind == 'api':
                name = f'field-probe-{number}'
                def request(method, suffix='', data=None):
                    began = time.monotonic()
                    try:
                        req = urllib.request.Request(api[0] + suffix, data=json.dumps(data).encode() if data is not None else None,
                            method=method, headers={'Content-Type': 'application/json'})
                        with urllib.request.urlopen(req, context=api[1], timeout=2) as response:
                            return json.load(response)
                    finally:
                        record.setdefault('api_operations', []).append({'method': method, 'latency_seconds': time.monotonic()-began})
                try:
                    request('POST', data={'apiVersion':'v1','kind':'ConfigMap','metadata':{'name':name},'data':{'number':str(number)}})
                    value = request('GET', '/' + name)['data']['number']
                    record['ok'] = value == str(number)
                    if not record['ok']: record['error'] = 'ConfigMap read mismatch'
                finally:
                    try: request('DELETE', '/' + name)
                    except Exception as exc: record['cleanup_error'] = str(exc)
                return
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
            if args.stop_file and args.stop_file.exists(): break
            scheduled = tick / 20
            time.sleep(max(0, origin + scheduled - time.monotonic()))
            probes = [('http', tick)] + ([('append', args.id_base + tick // 2)] if tick % 2 == 0 else [])
            if args.kubeconfig and tick % 100 == 0:
                probes.append(('api', tick // 100))
            for kind, number in probes:
                if slots.acquire(blocking=False):
                    pool.submit(probe, kind, number, scheduled)
                else:
                    log({'kind': kind, 'id': number, 'ok': False, 'acknowledged': False,
                         'scheduled_seconds': scheduled, 'error': 'driver_overload'})
            tick += 1
    out.close()
    args.output.with_suffix(args.output.suffix + ".done").touch()
    if credentials_dir: credentials_dir.cleanup()


if __name__ == '__main__':
    main()
