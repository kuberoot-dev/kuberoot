#!/usr/bin/env python3
"""R6 on kuberoot r2: FirewallRule scale through the router API.

Probes run on the gateway VM (transit 10.255.0.3) toward c2 (r2 LAN) and cross
r2's forward chain. The transit zone loses its blanket forwardTo, so only
FirewallRules let TCP through. Port 2399 is never allowed by any configuration;
any answer on it is a forbidden packet. Marker ports show which set is live.
"""
import base64, json, os, subprocess, sys, time
from pathlib import Path

raw = Path('/work/field/raw')
KC = '/work/cluster-r2.conf'
TARGET = '192.168.20.113'
FORBIDDEN = 2399
rows = []
sizes = [int(x) for x in sys.argv[1:]] or [100, 1000, 10000, 20000]


def log(*a):
    print(time.strftime('%H:%M:%S', time.gmtime()), *a, flush=True)


def kubectl(args, data=None, timeout=900, quiet=False):
    cmd = ['kubectl', '--request-timeout=60s'] + args
    if not quiet:
        log('$', ' '.join(cmd) + (' < generated list' if data else ''))
    p = subprocess.run(cmd, env=dict(os.environ, KUBECONFIG=KC), input=data,
                       capture_output=True, text=True, timeout=timeout)
    if not quiet:
        tail = (p.stdout + p.stderr).strip().splitlines()
        print('\n'.join(tail[-4:]), flush=True)
    return p


def guest(cmd, timeout=60, quiet=False):
    if not quiet:
        log('$ guest-command gateway', cmd)
    p = subprocess.run(['/work/field/guest-command', 'gateway', cmd], capture_output=True, text=True, timeout=timeout)
    if not quiet:
        print((p.stdout + p.stderr).strip(), flush=True)
    return p


CHECK = ("python3 -c 'import socket,sys\ns=socket.socket();s.settimeout(0.4)\n"
         "try:\n s.connect((\"%s\",%d));print(1)\nexcept ConnectionRefusedError:print(1)\nexcept Exception:print(0)'")


def passes(port):
    p = guest(CHECK % (TARGET, port), quiet=True)
    return p.stdout.strip() == '1'


def wait_marker(port, want=True, limit=600):
    t = time.monotonic()
    while time.monotonic() - t < limit:
        if passes(port) == want:
            return time.monotonic() - t
        time.sleep(0.2)
    return None


def rule(name, port, extra=None):
    spec = {'priority': 100, 'from': 'transit', 'to': 'lan', 'protocol': 'tcp', 'ports': [port], 'action': 'Accept'}
    if extra:
        spec.update(extra)
    return {'apiVersion': 'router.kuberoot.dev/v1alpha1', 'kind': 'FirewallRule',
            'metadata': {'name': name, 'labels': {'field': 'r6'}}, 'spec': spec}


def ruleset(n, marker):
    items = [rule(f'r6-{n}-{i:05d}', 40000 + (i % 20000), {'sources': [f'198.51.{(i >> 8) & 255}.{i & 255}/32']}) for i in range(n - 1)]
    items.append(rule(f'r6-{n}-allow', marker))
    return json.dumps({'apiVersion': 'v1', 'kind': 'List', 'items': items})


def delete_all():
    t = time.monotonic()
    p = kubectl(['delete', '--raw', '/apis/router.kuberoot.dev/v1alpha1/firewallrules?labelSelector=field%3Dr6'])
    for i in range(600):
        c = kubectl(['get', 'firewallrules', '-l', 'field=r6', '-o', 'name'], quiet=True)
        if c.returncode == 0 and not c.stdout.strip():
            break
        if i % 30 == 29:  # a collection delete cut by the client timeout stops server-side; issue it again
            kubectl(['delete', '--raw', '/apis/router.kuberoot.dev/v1alpha1/firewallrules?labelSelector=field%3Dr6'], quiet=True)
        time.sleep(1)
    return time.monotonic() - t, p.returncode


def nodemem():
    p = kubectl(['get', '--raw', '/api/v1/nodes'], quiet=True)
    return p.returncode


# --- fixture ---------------------------------------------------------------
guest('ip route replace 192.168.20.0/24 via 10.255.0.2 dev enp0s6')
code = base64.b64encode(Path('/work/field/tcp-port-probes.py').read_bytes()).decode()
guest(f'echo {code} | base64 -d >/root/tcp-port-probes.py', quiet=True)
kubectl(['apply', '-f', '-'], json.dumps({'apiVersion': 'router.kuberoot.dev/v1alpha1', 'kind': 'FirewallRule',
        'metadata': {'name': 'field-r6-icmp', 'labels': {'field': 'r6-fixture'}},
        'spec': {'priority': 10, 'from': 'transit', 'to': 'lan', 'protocol': 'icmp', 'action': 'Accept'}}))
kubectl(['patch', 'firewallzone', 'transit', '--type=merge', '-p', '{"spec":{"forwardTo":[]}}'])
delete_all()
if wait_marker(2201, want=False, limit=30) is None:
    raise SystemExit('default deny not effective; no measurement')
total = 7200  # upper bound; stopped explicitly at the end
guest(f'nohup python3 /root/tcp-port-probes.py --target {TARGET} --forbidden {FORBIDDEN} --markers 2200-2215 '
      f'--seconds {total} --output /root/R6-kuberoot-probes.jsonl >/root/R6-probe.log 2>&1 </dev/null &')
t_probe0 = time.time()

marker = 2200
for n in sizes:
    marker += 1
    entry = {'count': n}
    log(f'== {n} rules: full replace (delete old set, create new set with final allow {marker})')
    entry['delete_s'], _ = delete_all()
    data = ruleset(n, marker)
    t0 = time.monotonic(); w0 = time.time()
    p = kubectl(['create', '-f', '-'], data, timeout=3600, quiet=True)
    entry['api_accept_s'] = time.monotonic() - t0
    entry['create_exit'] = p.returncode
    entry['create_errors'] = [l for l in (p.stdout + p.stderr).splitlines() if 'rror' in l][:5]
    log(f'api accepted {n} objects in {entry["api_accept_s"]:.1f}s exit={p.returncode}')
    k = wait_marker(marker, True, 900)
    entry['kernel_effect_from_create_start_s'] = None if k is None else time.monotonic() - t0
    entry['create_started_wall'] = w0
    log(f'marker {marker} forwarded {entry["kernel_effect_from_create_start_s"]}s after create began')
    st = kubectl(['get', 'firewallrule', f'r6-{n}-allow', '-o', 'jsonpath={.status.conditions}'], quiet=True)
    entry['allow_status'] = st.stdout

    # one-rule edit: move the final allow to a new marker
    edit = marker + 8
    t0 = time.monotonic(); w0 = time.time()
    p = kubectl(['patch', 'firewallrule', f'r6-{n}-allow', '--type=merge', '-p', json.dumps({'spec': {'ports': [edit]}})])
    entry['edit_api_s'] = time.monotonic() - t0
    k = wait_marker(edit, True, 300)
    entry['edit_kernel_s'] = None if k is None else time.monotonic() - t0
    entry['edit_started_wall'] = w0
    log(f'one-rule edit: api {entry["edit_api_s"]:.2f}s, kernel {entry["edit_kernel_s"]}s')

    # unrelated metadata change
    t0 = time.monotonic(); w0 = time.time()
    p = kubectl(['annotate', 'firewallrule', f'r6-{n}-00000', 'field.kuberoot.dev/description=unrelated-change', '--overwrite'])
    entry['description_api_s'] = time.monotonic() - t0
    entry['description_wall'] = w0
    time.sleep(5)

    # invalid input: schema-invalid and schema-valid-but-unresolvable
    p = kubectl(['create', '-f', '-'], json.dumps(rule(f'r6-{n}-bad-action', 2398, {'action': 'Allow'})))
    entry['invalid_schema'] = {'exit': p.returncode, 'out': (p.stdout + p.stderr).strip()[-300:]}
    t0 = time.monotonic(); w0 = time.time()
    p = kubectl(['create', '-f', '-'], json.dumps(rule(f'r6-{n}-bad-zone', 2398, {'from': 'no-such-zone'})))
    entry['invalid_zone_create'] = {'exit': p.returncode, 'out': (p.stdout + p.stderr).strip()[-300:]}
    time.sleep(8)
    st = kubectl(['get', 'firewallrule', f'r6-{n}-bad-zone', '-o', 'jsonpath={.status.conditions}'])
    entry['invalid_zone_status'] = st.stdout
    entry['invalid_zone_marker_still_passes'] = passes(edit)
    entry['invalid_window_wall'] = [w0, time.time()]
    kubectl(['delete', 'firewallrule', f'r6-{n}-bad-zone', '--ignore-not-found'])

    # reboot with this set
    w0 = time.time(); t0 = time.monotonic()
    log('$ python3 /work/field/qmp.py r2 system_reset')
    subprocess.run(['python3', '/work/field/qmp.py', 'r2', 'system_reset'], check=True)
    time.sleep(3)
    k = wait_marker(edit, True, 600)
    entry['reboot_marker_back_s'] = None if k is None else time.monotonic() - t0
    entry['reboot_wall'] = w0
    log(f'after reboot marker {edit} forwarded again at {entry["reboot_marker_back_s"]}s')
    for _ in range(120):
        if kubectl(['get', 'firewallrules', '-o', 'name'], quiet=True).returncode == 0:
            break
        time.sleep(2)
    entry['api_back_s'] = time.monotonic() - t0
    rows.append(entry)
    (raw / 'R6-kuberoot-scale.json').write_text(json.dumps(rows, indent=2) + '\n')

# API connection cut in the middle of a multi-object create
log('== API cut: create 1000-rule set, kill the client after 1.5 s')
delete_all()
data = ruleset(1000, 2214)
w0 = time.time()
proc = subprocess.Popen(['kubectl', '--request-timeout=60s', 'create', '-f', '-'], env=dict(os.environ, KUBECONFIG=KC),
                        stdin=subprocess.PIPE, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, text=True)
proc.stdin.write(data); proc.stdin.close(); time.sleep(1.5); proc.kill(); proc.wait()
time.sleep(10)
c = kubectl(['get', 'firewallrules', '-l', 'field=r6', '-o', 'name'], quiet=True)
cut = {'created_before_cut': len(c.stdout.split()), 'final_allow_present': 'r6-1000-allow' in c.stdout,
       'marker_2214_passes': passes(2214), 'wall': w0}
log('after cut:', json.dumps(cut))
(raw / 'R6-kuberoot-apicut.json').write_text(json.dumps(cut, indent=2) + '\n')

time.sleep(5)
# wait for the probe run to finish, then fetch it
guest('pkill -INT -f tcp-port-probes.py; sleep 2')
data = subprocess.check_output(['/work/field/guest-command', 'gateway', 'cat /root/R6-kuberoot-probes.jsonl'])
(raw / 'R6-kuberoot-probes.jsonl').write_bytes(data)
probes = [json.loads(l) for l in data.decode().splitlines() if l.strip()]
forb = [x for x in probes if x['kind'] == 'forbidden']
leaks = [x for x in forb if x['result'] == 'answered']
summary = {'probes': len(probes), 'forbidden_probes': len(forb), 'forbidden_answered': len(leaks),
           'forbidden_errors': sum(1 for x in forb if x['result'].startswith('error')),
           'leak_times': [x['t'] for x in leaks[:50]], 'rows': rows, 'api_cut': cut}
(raw / 'R6-kuberoot.summary.json').write_text(json.dumps(summary, indent=2) + '\n')
log(json.dumps({k: summary[k] for k in ['probes', 'forbidden_probes', 'forbidden_answered']}))
# restore fixture
delete_all()
kubectl(['patch', 'firewallzone', 'transit', '--type=merge', '-p', '{"spec":{"forwardTo":["lan"]}}'])
kubectl(['delete', 'firewallrule', 'field-r6-icmp', '--ignore-not-found'])
