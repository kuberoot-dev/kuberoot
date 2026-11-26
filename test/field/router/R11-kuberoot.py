#!/usr/bin/env python3
"""R11 on kuberoot r2 (no Safeguard trial): state writes and storage faults.

A: offered 10 config updates/s (Route metric patches through the router API)
   for --seconds; guest disk writes from QMP blockstats -> bytes/day.
B: QEMU IOPS throttle 50 then 10 on r2's disk for 300 s each while updates
   continue at 1/s; forwarding gateway -> r2 -> c2 is pinged throughout.
C: hard stop right after an acknowledged write (QEMU kill -9), then check the
   object survived and the node applied it.
"""
import argparse, json, os, signal, subprocess, threading, time
from pathlib import Path

ap = argparse.ArgumentParser(); ap.add_argument('--seconds', type=int, default=1200); a = ap.parse_args()
raw = Path('/work/field/raw'); KC = '/work/cluster-r2.conf'; env = dict(os.environ, KUBECONFIG=KC)
out = {}


def say(*x):
    print(time.strftime('%H:%M:%S', time.gmtime()), *x, flush=True)


def kubectl(args, data=None, timeout=15, quiet=True):
    if not quiet:
        say('$ kubectl', ' '.join(args))
    try:
        p = subprocess.run(['kubectl', '--request-timeout=10s'] + args, env=env, input=data, capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        p = subprocess.CompletedProcess([], 124, '', 'timeout')
    if not quiet:
        print((p.stdout + p.stderr).strip()[-400:], flush=True)
    return p


def writes():
    q = json.loads(subprocess.check_output(['python3', '/work/field/qmp.py', 'r2', 'query-blockstats']))
    return sum(b['stats']['wr_bytes'] for b in q), sum(b['stats']['wr_operations'] for b in q)


def disk_qdev():
    for b in json.loads(subprocess.check_output(['python3', '/work/field/qmp.py', 'r2', 'query-block'])):
        if b.get('inserted') and 'pflash' not in b.get('device', '') and not b.get('removable'):
            return b.get('qdev') or b['device']


def throttle(iops):
    dev = disk_qdev(); say(f'$ qmp r2 block_set_io_throttle iops={iops} ({dev})')
    arg = {'bps': 0, 'bps_rd': 0, 'bps_wr': 0, 'iops': iops, 'iops_rd': 0, 'iops_wr': 0}
    for key in ('id', 'device'):
        p = subprocess.run(['python3', '/work/field/qmp.py', 'r2', 'block_set_io_throttle', json.dumps(dict(arg, **{key: dev}))], capture_output=True, text=True)
        if p.returncode == 0:
            return
    raise SystemExit('throttle failed: ' + p.stderr)


route = lambda m: json.dumps({'apiVersion': 'router.kuberoot.dev/v1alpha1', 'kind': 'Route', 'metadata': {'name': 'field-r11'},
                               'spec': {'destination': '198.51.103.0/24', 'gateway': '10.255.0.1', 'link': 'eth2', 'metric': m}})


def update_loop(seconds, rate, tag):
    lat = []; errs = 0; n = 0; t0 = time.monotonic()
    while time.monotonic() - t0 < seconds:
        t = time.monotonic(); n += 1
        p = kubectl(['apply', '-f', '-'], route(100 + n % 1000))
        lat.append(time.monotonic() - t); errs += p.returncode != 0
        if n % (rate * 60) == 0:
            say(f'{tag}: {n} updates, errors {errs}, last-minute p99 {sorted(lat[-rate * 60:])[int(rate * 60 * .99) - 1]:.3f}s')
        time.sleep(max(0, t0 + n / rate - time.monotonic()))
    el = time.monotonic() - t0; lat.sort()
    return {'offered_per_s': rate, 'updates': n, 'errors': errs, 'achieved_per_s': n / el, 'seconds': el,
            'p50_s': lat[len(lat) // 2], 'p99_s': lat[int(len(lat) * .99) - 1], 'max_s': lat[-1]}


kubectl(['apply', '-f', '-'], route(100), quiet=False)
# A: write rate
w0, o0 = writes(); t0 = time.monotonic()
say(f'== A: offered 10 updates/s for {a.seconds}s')
r = update_loop(a.seconds, 10, 'A')
w1, o1 = writes(); el = time.monotonic() - t0
r.update(guest_write_bytes=w1 - w0, guest_write_ops=o1 - o0, extrapolated_bytes_per_day=(w1 - w0) * 86400 / el)
out['A-10-per-s'] = r; say('A', json.dumps(r))
(raw / 'R11-kuberoot.summary.json').write_text(json.dumps(out, indent=2) + '\n')

# B: IOPS throttles while forwarding is observed
for iops in (50, 10):
    subprocess.run(['/work/field/guest-command', 'gateway', 'pkill fping; rm -f /root/R11-fwd.txt; nohup timeout 320 fping -D -l -p 200 192.168.20.113 >/root/R11-fwd.txt 2>&1 </dev/null &'])
    throttle(iops); say(f'== B: {iops} IOPS for 300s, 1 update/s')
    r = update_loop(300, 1, f'B{iops}')
    throttle(0); time.sleep(25)
    fwd = subprocess.run(['/work/field/guest-command', 'gateway', 'cat /root/R11-fwd.txt'], capture_output=True, text=True).stdout
    (raw / f'R11-kuberoot-iops{iops}-fwd.txt').write_text(fwd)
    st = kubectl(['get', 'route', 'field-r11', '-o', 'jsonpath={.status.conditions[0].reason} {.status.observedGeneration} {.metadata.generation}'])
    r.update(fwd_replies=fwd.count(' bytes, '), fwd_timeouts=fwd.count('timed out') + fwd.count('unreachable'), status_after=st.stdout)
    out[f'B-iops-{iops}'] = r; say(f'B{iops}', json.dumps(r))
    (raw / 'R11-kuberoot.summary.json').write_text(json.dumps(out, indent=2) + '\n')

# C: hard stop right after an acknowledged write
marker = int(time.time()) % 100000
p = kubectl(['apply', '-f', '-'], route(5000 + marker % 1000), quiet=False)
acked = p.returncode == 0
pid = int(Path('/work/vms/r2/pid').read_text()); argv = [x.decode() for x in Path(f'/proc/{pid}/cmdline').read_bytes().split(b'\0') if x]
os.kill(pid, signal.SIGKILL); cut = time.monotonic(); say('$ kill -9 qemu r2 (power cut right after the acknowledged apply)')
for _ in range(50):
    if not Path(f'/proc/{pid}').exists():
        break
    time.sleep(.1)
Path('/work/vms/r2/pid').unlink(missing_ok=True)
for _ in range(15):
    q = subprocess.run(argv, capture_output=True, text=True)
    if q.returncode == 0:
        break
    time.sleep(1)
back = None
while time.monotonic() - cut < 300:
    s = kubectl(['get', 'route', 'field-r11', '-o', 'json'])
    if s.returncode == 0:
        back = time.monotonic() - cut; break
    time.sleep(2)
time.sleep(20)
s = kubectl(['get', 'route', 'field-r11', '-o', 'json'])
obj = json.loads(s.stdout) if s.returncode == 0 else {}
out['C-powercut-after-ack'] = {'acked': acked, 'expected_metric': 5000 + marker % 1000, 'metric_after': obj.get('spec', {}).get('metric'),
                               'applied_after': [c for c in obj.get('status', {}).get('conditions', [])], 'api_back_s': back}
say('C', json.dumps(out['C-powercut-after-ack']))
(raw / 'R11-kuberoot.summary.json').write_text(json.dumps(out, indent=2) + '\n')
kubectl(['delete', 'route', 'field-r11'], quiet=False)
