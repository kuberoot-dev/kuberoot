#!/usr/bin/env python3
"""R10 (remaining case) on kuberoot r2: a zone removed, then a reboot before the
router controller has applied anything.

Case 1: FirewallZone g-wan (eth4, G WAN) deleted and confirmed; reboot.
Case 2: FirewallZone transit (eth2) deleted and confirmed; reboot. eth2 gets
        its address from the kernel command line (kuberoot.ip), i.e. before
        any router configuration exists.
During each reboot the gateway probes the DUT every 0.25 s from the affected
segment: TCP connect to the kube API (6443), node API (50000), SSH (22),
ICMP to the DUT, and forwarding through the DUT to the G client.
Any success on an unzoned link is an exposure. Management reachability over
the declared management zone (wan, eth0 via host forward) is tracked too.
"""
import json, os, subprocess, time
from pathlib import Path

VM = os.environ.get('R10_VM', 'r2'); TAG = os.environ.get('R10_TAG', '')  # r11 for a test build
raw = Path('/work/field/raw'); env = dict(os.environ, KUBECONFIG=f'/work/cluster-{VM}.conf'); out = {}

PROBE = r'''
import json, socket, subprocess, sys, threading, time
targets = json.loads(sys.argv[1]); end = time.time() + float(sys.argv[2])
def probe(row, name, host, port):
    if port == 'icmp':
        row[name] = subprocess.run(['ping', '-c1', '-W1', host], capture_output=True).returncode == 0; return
    s = socket.socket(); s.settimeout(.5)
    try:
        s.connect((host, port)); row[name] = 'open'
    except ConnectionRefusedError:
        row[name] = 'refused'
    except OSError:
        row[name] = 'silent'
    finally:
        s.close()
while time.time() < end:
    row = {'at': time.time()}
    ts = [threading.Thread(target=probe, args=(row, n, h, p)) for n, (h, p) in targets.items()]
    [t.start() for t in ts]; [t.join() for t in ts]
    print(json.dumps(row), flush=True)
    time.sleep(.25)
'''


def say(*a):
    print(time.strftime('%H:%M:%S', time.gmtime()), *a, flush=True)


def kubectl(a, data=None, quiet=False, timeout=30):
    if not quiet:
        say('$ kubectl', ' '.join(a) + (' < generated' if data else ''))
    try:
        p = subprocess.run(['kubectl', '--request-timeout=8s'] + a, env=env, input=data, capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        p = subprocess.CompletedProcess([], 124, '', 'timeout')
    if not quiet and (p.stdout + p.stderr).strip():
        print((p.stdout + p.stderr).strip()[-500:], flush=True)
    return p


def guest(vm, cmd, quiet=False, data=None, timeout=60):
    if not quiet:
        say('$ guest-command', vm, cmd[:150])
    p = subprocess.run(['/work/field/guest-command', vm, cmd], input=data, capture_output=True, text=True, timeout=timeout)
    if not quiet and (p.stdout + p.stderr).strip():
        print((p.stdout + p.stderr).strip()[-400:], flush=True)
    return p


def confirm_pending():
    for _ in range(20):
        p = kubectl(['get', 'safeguard', 'default', '-o', 'json'], quiet=True)
        try:
            st = json.loads(p.stdout)['status']
        except Exception:
            time.sleep(1); continue
        if st.get('pending'):
            kubectl(['patch', 'safeguard', 'default', '--type=merge', '-p', json.dumps({'spec': {'confirm': st['pending']}})])
            return st['pending']
        time.sleep(1)


def zone(name):
    p = kubectl(['get', 'firewallzone', name, '-o', 'json'], quiet=True)
    o = json.loads(p.stdout); return {'apiVersion': o['apiVersion'], 'kind': o['kind'], 'metadata': {'name': name}, 'spec': o['spec']}


def api_ready():
    return kubectl(['get', '--raw', '/readyz'], quiet=True, timeout=10).returncode == 0


guest('gateway', 'cat >/root/R10-probe.py', data=PROBE, quiet=True)
cases = [('g-wan-removed', 'g-wan', {'api': ['198.18.0.1', 6443], 'node': ['198.18.0.1', 50000], 'ssh': ['198.18.0.1', 22],
                                     'dut-icmp': ['198.18.0.1', 'icmp'], 'forward-to-client': ['192.168.10.2', 'icmp']}, 'test0'),
         ('transit-removed', 'transit', {'api': ['10.255.0.2', 6443], 'node': ['10.255.0.2', 50000], 'ssh': ['10.255.0.2', 22],
                                         'dut-icmp': ['10.255.0.2', 'icmp'], 'dut-lan-address-icmp': ['192.168.20.1', 'icmp']}, 'enp0s6')]
for tag, zname, targets, dev in [c for c in cases if c[0] in os.environ.get('R10_CASES', 'g-wan-removed,transit-removed').split(',')]:
    saved = zone(zname)
    (raw / f'R10-{VM}{TAG}-{tag}-zone-before.json').write_text(json.dumps(saved, indent=2) + '\n')
    guest('gateway', f"timeout 15 python3 /root/R10-probe.py '{json.dumps(targets)}' 10 {dev} | tail -1")
    kubectl(['delete', 'firewallzone', zname]); time.sleep(3)
    rev = confirm_pending(); time.sleep(8)
    guest('gateway', f"timeout 15 python3 /root/R10-probe.py '{json.dumps(targets)}' 5 {dev} | tail -1")
    kubectl(['get', 'firewallzones', '-o', 'wide'])
    guest('gateway', f"rm -f /root/R10-{tag}.jsonl; nohup timeout 200 python3 /root/R10-probe.py '{json.dumps(targets)}' 190 {dev} >/root/R10-{tag}.jsonl 2>&1 </dev/null &", quiet=True)
    time.sleep(3)
    reset_at = time.time()
    say(f'$ python3 /work/field/qmp.py {VM} system_reset  # reboot with the zone gone')
    subprocess.run(['python3', '/work/field/qmp.py', VM, 'system_reset'], check=True, capture_output=True)
    time.sleep(5); ready = None
    while time.time() - reset_at < 180:
        if api_ready():
            ready = time.time() - reset_at; break
        time.sleep(1)
    say(f'management API (wan zone, eth0) ready {ready:.1f}s after reset' if ready else 'management API not ready in 180 s')
    while time.time() - reset_at < 195:
        time.sleep(2)
    rows = [json.loads(l) for l in guest('gateway', f'cat /root/R10-{tag}.jsonl', quiet=True).stdout.splitlines() if l.startswith('{')]
    (raw / f'R10-{VM}{TAG}-{tag}-probes.jsonl').write_text(''.join(json.dumps(r) + '\n' for r in rows))
    res = {}
    for k in targets:
        ok = [r['at'] - reset_at for r in rows if r.get(k) in (True, 'open')]
        refused = [r['at'] - reset_at for r in rows if r.get(k) == 'refused']
        res[k] = {'samples': len(rows), 'successes': len(ok), 'first_success_s': round(ok[0], 2) if ok else None,
                  'refused': len(refused), 'first_refused_s': round(refused[0], 2) if refused else None}
    out[tag] = {'zone': zname, 'confirmed_revision': rev, 'management_api_ready_s': ready, 'probes': res}
    say(tag, json.dumps(res))
    kubectl(['get', 'firewallzones', '-o', 'wide'])
    kubectl(['apply', '-f', '-'], json.dumps(saved)); time.sleep(3); confirm_pending(); time.sleep(8)
    guest('gateway', f"timeout 15 python3 /root/R10-probe.py '{json.dumps(targets)}' 5 {dev} | tail -1")
(raw / f'R10-{VM}{TAG}-zone-reboot.summary.json').write_text(json.dumps(out, indent=2) + '\n')
say('done')
