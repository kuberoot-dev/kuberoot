#!/usr/bin/env python3
"""R1(c) on kuberoot r1 with management on a tagged VLAN.

The untagged variant cannot lock out: Linux answers ARP for 10.255.0.1 on eth2
even after the address moves to eth2.999 (weak host model). Here management
lives only on VLAN 100 (r1 eth2.100 10.255.100.1 <-> gateway enp0s6.100
10.255.100.3); the fault re-tags it to VLAN 999, which the gateway does not carry.
The production kubeconfig reaches 10.255.100.1:6443 through socat on the gateway
and an SSH tunnel from the pod; the observer kubeconfig only reads and confirms.
"""
import argparse, json, os, subprocess, time
from pathlib import Path

ap = argparse.ArgumentParser(); ap.add_argument('mode', choices=['normal', 'reboot', 'controller']); a = ap.parse_args()
raw = Path('/work/field/raw'); name = 'R1-kuberoot-vlan-tagged-' + a.mode; rows = []
PROD = '/work/field/private/r1-production-vlan.conf'


def say(*x):
    print(time.strftime('%FT%TZ', time.gmtime()), *x, flush=True)


def run(args, obj=None, observer=False, quiet=False):
    cmd = ['kubectl', '--request-timeout=2s'] + args
    env = dict(os.environ, KUBECONFIG='/work/cluster-r1.conf' if observer else PROD)
    if not quiet:
        say('$', ' '.join(cmd))
    p = subprocess.run(cmd, env=env, input=json.dumps(obj) if obj else None, capture_output=True, text=True, timeout=8)
    rows.append({'at': time.time(), 'observer': observer, 'command': cmd, 'exit': p.returncode, 'stdout': p.stdout, 'stderr': p.stderr})
    if not quiet:
        print((p.stdout + p.stderr).strip()[-600:], flush=True)
    return p


def gw(cmd):
    say('$ guest-command gateway', cmd)
    p = subprocess.run(['/work/field/guest-command', 'gateway', cmd], capture_output=True, text=True, timeout=30)
    print((p.stdout + p.stderr).strip(), flush=True)
    return p


def obj(kind, n, spec):
    return {'apiVersion': 'router.kuberoot.dev/v1alpha1', 'kind': kind, 'metadata': {'name': n}, 'spec': spec}


vlan = obj('Interface', 'field-mgmt-vlan', {'link': 'eth2.100', 'vlan': {'parent': 'eth2', 'id': 100}, 'addresses': ['10.255.100.1/24']})

# fixture: gateway VLAN 100 + socat, pod tunnel, production kubeconfig
gw('ip link show enp0s6.100 >/dev/null 2>&1 || ip link add link enp0s6 name enp0s6.100 type vlan id 100; '
   'ip addr replace 10.255.100.3/24 dev enp0s6.100; ip link set enp0s6.100 up; '
   'pgrep -f "TCP-LISTEN:16444" >/dev/null || systemd-run --unit field-proxy-16444 socat TCP-LISTEN:16444,bind=127.0.0.1,reuseaddr,fork TCP:10.255.100.1:6443')
if subprocess.run(['bash', '-c', 'ss -ltn | grep -q 127.0.0.1:16444']).returncode:
    subprocess.run(['ssh', '-f', '-N', '-L', '127.0.0.1:16444:127.0.0.1:16444', '-o', 'ExitOnForwardFailure=yes', '-o', 'StrictHostKeyChecking=no',
                    '-o', 'UserKnownHostsFile=/dev/null', '-i', '/work/field/private/id_ed25519', '-p', '2232', 'root@127.0.0.1'], check=True)
if not Path(PROD).exists():
    s = Path('/work/field/private/r1-production.conf').read_text().replace('https://127.0.0.1:16443', 'https://127.0.0.1:16444')
    Path(PROD).write_text(s); os.chmod(PROD, 0o600)

run(['delete', 'interface', 'field-bad-vlan', '--ignore-not-found'], observer=True)
run(['apply', '-f', '-'], vlan, True)
run(['patch', 'firewallzone', 'transit', '--type=merge', '-p', '{"spec":{"links":["eth2","eth2.100"],"input":"Accept"}}'], observer=True)


def confirm_baseline():
    for _ in range(60):
        p = run(['get', 'interface', 'field-mgmt-vlan', '-o', 'json'], observer=True, quiet=True)
        if p.returncode == 0:
            r = json.loads(p.stdout); st = r.get('status', {})
            if st.get('observedGeneration') == r['metadata']['generation'] and any(c['type'] == 'Applied' and c['status'] == 'True' for c in st.get('conditions', [])):
                break
        time.sleep(1)
    else:
        raise SystemExit('baseline VLAN not applied')
    time.sleep(2)
    st = json.loads(run(['get', 'safeguard', 'default', '-o', 'json'], observer=True, quiet=True).stdout)['status']; rev = st.get('pending') or st['running']
    run(['patch', 'safeguard', 'default', '--type=merge', '-p', json.dumps({'spec': {'confirmWithin': '60s', 'confirm': rev}})], observer=True)
    for _ in range(20):
        st = json.loads(run(['get', 'safeguard', 'default', '-o', 'json'], observer=True, quiet=True).stdout)['status']
        if st.get('confirmed') == rev and st.get('running') == rev and not st.get('pending'):
            return rev
        time.sleep(1)
    raise SystemExit('baseline confirmation not observed')


base = confirm_baseline()
for _ in range(30):
    if run(['get', 'interface', 'field-mgmt-vlan', '-o', 'name']).returncode == 0:
        break
    time.sleep(2)
else:
    raise SystemExit('production path over VLAN 100 not reachable; no fault injected')

start = time.monotonic(); wall = time.time(); lost = False; recovered = None; injected = False
bad = json.loads(json.dumps(vlan)); bad['spec'] = {'link': 'eth2.999', 'vlan': {'parent': 'eth2', 'id': 999}, 'addresses': ['10.255.100.1/24']}
run(['apply', '-f', '-'], bad)
while time.monotonic() - start < 170:
    el = time.monotonic() - start
    if a.mode != 'normal' and el >= 20 and not injected:
        cmd = ['python3', '/work/field/qmp.py', 'r1', 'system_reset'] if a.mode == 'reboot' else \
              ['python3', '/work/field/console.py', 'r1', 'old=$(/bin/busybox pidof kuberoot-router); printf "before_pid=%s\\n" "$old"; kill -KILL "$old"; /bin/busybox sleep 2; printf "after_pid=%s\\n" "$(/bin/busybox pidof kuberoot-router)"']
        say('$', ' '.join(cmd)); p = subprocess.run(cmd, capture_output=True, text=True, timeout=40); print(p.stdout[-300:], flush=True); injected = True
    p = run(['get', 'safeguard', 'default', '-o', 'json'], quiet=True)
    if p.returncode:
        lost = True
    else:
        s = json.loads(p.stdout)['status']
        if lost and s.get('rolledBack') and not s.get('pending'):
            recovered = time.monotonic() - start; break
    print(json.dumps({'elapsed_s': round(time.monotonic() - start, 1), 'production_api': p.returncode == 0}), flush=True); time.sleep(2)
after = run(['get', 'interface', 'field-mgmt-vlan', '-o', 'jsonpath={.spec.vlan.id}'], observer=True)
summary = {'fault': 'vlan-tagged', 'mode': a.mode, 'lost': lost, 'recovered_s': recovered, 'fault_wall_time': wall,
           'automatic_recovery': recovered is not None, 'vlan_id_after': after.stdout, 'baseline_revision': base,
           'production_path': 'pod -> SSH tunnel -> socat on gateway -> enp0s6.100 (VLAN 100) -> r1 eth2.100 10.255.100.1:6443; no observer repair during trial'}
(raw / (name + '.json')).write_text(json.dumps(rows, indent=2) + '\n'); (raw / (name + '.summary.json')).write_text(json.dumps(summary, indent=2) + '\n')
print(json.dumps(summary), flush=True)
if recovered is None:
    raise SystemExit('No automatic recovery observed')
