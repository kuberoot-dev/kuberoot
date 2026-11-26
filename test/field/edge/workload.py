#!/usr/bin/env python3
"""Workload W: fixed 1 KiB HTTP response and fsync-before-ack append store.

Use separate deployments for HTTP (10 replicas) and append (1 replica, PVC).
The append file is framed in fixed 1024-byte records. Recover a torn final
record by truncation before accepting further requests. IDs are supplied by
an outside observer; a timeout means unknown outcome, never acknowledged.
"""
import argparse
import http.server
import os
import pathlib
import threading

BODY = b'W' * 1024
LOCK = threading.Lock()


def recover(path):
    fd = os.open(path, os.O_CREAT | os.O_RDWR, 0o600)
    size = os.fstat(fd).st_size
    os.ftruncate(fd, size - size % 1024)
    os.fsync(fd)
    os.close(fd)
    # Persist creation of the directory entry as well as file contents.
    fd = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
    os.fsync(fd)
    os.close(fd)


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def respond(self, code, data):
        self.send_response(code)
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        if self.path == '/http':
            self.respond(200, BODY)
        elif self.path == '/records' and self.server.records is not None:
            with LOCK:
                data = self.server.records.read_bytes()
            self.respond(200, data)
        else:
            self.respond(404, b'unknown endpoint')

    def do_POST(self):
        if self.path != '/append' or self.server.records is None:
            self.respond(404, b'unknown endpoint')
            return
        if self.headers.get('Content-Length') != '1024':
            self.respond(400, b'exactly 1024 bytes required')
            return
        data = self.rfile.read(1024)
        if len(data) != 1024 or not data[:20].isdigit():
            self.respond(400, b'20-digit record ID required')
            return
        with LOCK:
            fd = os.open(self.server.records, os.O_WRONLY | os.O_APPEND)
            try:
                view = memoryview(data)
                while view:
                    n = os.write(fd, view)
                    if n <= 0:
                        raise OSError('short write')
                    view = view[n:]
                os.fsync(fd)
            finally:
                os.close(fd)
        self.respond(200, data[:20])


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--port', type=int, default=8080)
    ap.add_argument('--records', type=pathlib.Path)
    args = ap.parse_args()
    if args.records is not None:
        args.records.parent.mkdir(parents=True, exist_ok=True)
        recover(args.records)
    server = http.server.ThreadingHTTPServer(('0.0.0.0', args.port), Handler)
    server.records = args.records
    server.serve_forever()


if __name__ == '__main__':
    main()
