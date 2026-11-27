#!/usr/bin/env python3
"""TCP SYN probes through a DUT firewall: any answer (SYN-ACK or RST) means the
packet was forwarded; a timeout means it was dropped. Half of the probes go to a
port that no configuration ever allows (forbidden), the rest rotate over marker
ports whose state tells which ruleset the kernel is running."""
import argparse, json, signal, socket, sys, threading, time

p = argparse.ArgumentParser()
p.add_argument('--target', required=True)
p.add_argument('--forbidden', type=int, required=True)
p.add_argument('--markers', required=True, help='first-last, inclusive')
p.add_argument('--rate', type=int, default=100)
p.add_argument('--seconds', type=float, required=True)
p.add_argument('--output', required=True)
p.add_argument('--timeout', type=float, default=0.4)
p.add_argument('--refused-is-dropped', action='store_true', help='target listens on every probed port: RST comes from the DUT rejecting')
a = p.parse_args()
first, last = (int(x) for x in a.markers.split('-'))
markers = list(range(first, last + 1))
out = open(a.output, 'w', buffering=1)
lock = threading.Lock()


def probe(port, kind, sent):
    s = socket.socket()
    s.settimeout(a.timeout)
    try:
        s.connect((a.target, port))
        result = 'answered'
    except ConnectionRefusedError:
        result = 'refused' if a.refused_is_dropped else 'answered'
    except (socket.timeout, TimeoutError):
        result = 'dropped'
    except OSError as e:
        result = 'error:' + str(e.errno)
    finally:
        s.close()
    with lock:
        out.write(json.dumps({'t': sent, 'port': port, 'kind': kind, 'result': result}) + '\n')


signal.signal(signal.SIGINT, lambda *_: (time.sleep(a.timeout + 0.2), out.flush(), sys.exit(0)))
start = time.time()
i = 0
while time.time() - start < a.seconds:
    due = start + i / a.rate
    delay = due - time.time()
    if delay > 0:
        time.sleep(delay)
    if i % 2 == 0:
        port, kind = a.forbidden, 'forbidden'
    else:
        port, kind = markers[(i // 2) % len(markers)], 'marker'
    threading.Thread(target=probe, args=(port, kind, time.time()), daemon=True).start()
    i += 1
time.sleep(a.timeout + 0.2)
out.close()
