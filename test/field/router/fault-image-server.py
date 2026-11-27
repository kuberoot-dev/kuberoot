#!/usr/bin/env python3
"""Serve a real image file with deterministic download faults (port 8091;
8090 is the forwarding observer).

/full/<name>        the whole file
/disconnect/<name>  advertises the full length, sends 25 %, closes the connection
/truncated/<name>   advertises and sends only the first 25 % (a complete, short file)
/stall/<name>       sends 25 %, then holds the connection open without data
"""
import json, sys, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

root = Path(sys.argv[1] if len(sys.argv) > 1 else '/work/field/images')


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        parts = self.path.strip('/').split('/', 1)
        if len(parts) != 2 or parts[0] not in ('full', 'disconnect', 'truncated', 'stall') or '/' in parts[1]:
            self.send_error(404); return
        mode, name = parts; f = root / name
        if not f.is_file():
            self.send_error(404); return
        size = f.stat().st_size; send = size if mode == 'full' else size // 4
        self.send_response(200)
        self.send_header('Content-Length', str(size if mode in ('full', 'disconnect', 'stall') else send))
        self.end_headers()
        sent = 0
        with f.open('rb') as fh:
            while sent < send:
                chunk = fh.read(min(1 << 20, send - sent))
                if not chunk:
                    break
                try:
                    self.wfile.write(chunk)
                except OSError:
                    break
                sent += len(chunk)
        if mode == 'stall':
            time.sleep(600)
        self.close_connection = True
        print(json.dumps({'at': time.time(), 'path': self.path, 'served_bytes': sent, 'file_bytes': size}), flush=True)

    def log_message(self, *a):
        pass


ThreadingHTTPServer(('0.0.0.0', 8091), Handler).serve_forever()
