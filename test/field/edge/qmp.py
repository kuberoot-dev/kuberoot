#!/usr/bin/env python3
"""Send one QMP command, preserving actual response/error; no monitor shell."""
import json, socket, sys

def command(path, execute, arguments=None):
    with socket.socket(socket.AF_UNIX) as s:
        s.settimeout(10)
        s.connect(path)
        f = s.makefile('rwb', buffering=0)
        json.loads(f.readline())
        for ident, payload in [('cap', {'execute': 'qmp_capabilities'}),
                               ('cmd', {'execute': execute, 'arguments': arguments or {}})]:
            payload['id'] = ident
            f.write((json.dumps(payload) + '\n').encode())
            while True:
                reply = json.loads(f.readline())
                if reply.get('id') == ident:
                    if 'error' in reply:
                        raise RuntimeError(reply['error'])
                    break
        return reply['return']

if __name__ == '__main__':
    print(json.dumps(command(sys.argv[1], sys.argv[2], json.loads(sys.argv[3]) if len(sys.argv) > 3 else None), indent=2))
