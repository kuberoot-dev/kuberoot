#!/usr/bin/env python3
"""R9 for kuberoot r1: hard reset, carrier-up blackhole, flapping, bird restart.

Peers U1/U2 are BIRD instances on the gateway (transit segment). Probe target
10.64.0.1 is owned by the gateway and announced by both peers; probe1 on the
r1 LAN runs timestamped fping through r1. Timers/BFD are not configurable via
the BGPPeer API, so recovery uses the rendered BIRD defaults (recorded).
"""
import json, os, subprocess, time
from pathlib import Path

os.environ['KUBECONFIG'] = '/work/cluster-r1.conf'
raw = Path('/work/field/raw')
rows = []
events = []

def run(cmd, inp=None, timeout=40):
    print(time.strftime('%FT%TZ', time.gmtime()), '$', ' '.join(cmd), flush=True)
    p = subprocess.run(cmd, input=inp, capture_output=True, text=True, timeout=timeout)
    print(p.stdout + p.stderr, flush=True)
    rows.append({'at': time.time(), 'command': cmd, 'exit': p.returncode, 'stdout': p.stdout, 'stderr': p.stderr})
    return p

def g(cmd, timeout=40):
    return run(['/work/field/guest-command', 'gateway', cmd], timeout=timeout)

def k(args, obj=None):
    return run(['kubectl', '--request-timeout=5s'] + args, json.dumps(obj) if obj else None)

def bird_r1(cmd):
    return run(['python3', '/work/field/console.py', 'r1',
                f'/usr/sbin/birdc -s /run/kuberoot/router/bird.ctl {cmd}', '--timeout', '25'])

def event(phase, note=''):
    events.append({'at': time.time(), 'phase': phase, 'note': note})
    (raw / 'R9-kuberoot-events.json').write_text(json.dumps(events, indent=2) + '\n')

def apply_peer(name, addr, asn):
    k(['apply', '-f', '-'], {'apiVersion': 'router.kuberoot.dev/v1alpha1', 'kind': 'BGPPeer',
                             'metadata': {'name': name},
                             'spec': {'address': addr, 'asn': asn,
                                      'importPolicy': {'prefixes': ['10.64.0.0/24']},
                                      'exportPolicy': {'prefixes': []}}})

# --- setup ---
k(['patch', 'safeguard', 'default', '--type=merge', '-p', '{"spec":{"confirmWithin":"600s"}}'])
g('for p in /run/field-r9/*.pid; do test ! -f "$p" || kill $(cat "$p") 2>/dev/null; done; mkdir -p /run/field-r9; '
  'ip addr replace 10.64.0.1/32 dev enp0s6; ip route replace 192.168.10.0/24 via 10.255.0.1')
for name, local, asn in [('u1', '10.255.0.3', 65101), ('u2', '10.255.0.4', 65102)]:
    conf = (f'log stderr all; router id {local}; protocol device {{}}\n'
            f'protocol static injected {{ ipv4; route 10.64.0.1/32 blackhole; }}\n'
            f'protocol bgp dut {{ local {local} as {asn}; neighbor 10.255.0.1 as 65000; strict bind on; '
            f'ipv4 {{ import none; export where source = RTS_STATIC; }}; }}\n')
    run(['/work/field/guest-command', 'gateway', 'cat >/root/R9-' + name + '.conf'], conf)
    g(f'bird -c /root/R9-{name}.conf -s /run/field-r9/{name}.ctl -P /run/field-r9/{name}.pid')
    apply_peer('field-' + name, local, asn)
apply_peer('field-u1', '10.255.0.3', 65101)
for _ in range(30):
    p = k(['get', 'bgppeers', '-o', 'json'])
    try:
        st = {x['metadata']['name']: x['status'].get('state') for x in json.loads(p.stdout)['items']}
        if st.get('field-u1') == 'Established' and st.get('field-u2') == 'Established':
            break
    except Exception:
        pass
    time.sleep(2)
bird_r1('show protocols')
run(['/work/field/guest-command', 'probe1', 'ping -c 3 -W 2 10.64.0.1'])
event('setup-complete')

# --- continuous probe from probe1 through r1 ---
run(['/work/field/guest-command', 'probe1',
     'rm -f /tmp/R9-probes.txt; nohup timeout 700 fping -D -l -p 100 10.64.0.1 >/tmp/R9-probes.txt 2>&1 </dev/null &'])
time.sleep(5)

# --- phase 1: hard session reset (restart U1 bird) ---
event('hard-reset-start')
g('kill $(cat /run/field-r9/u1.pid); sleep 1; bird -c /root/R9-u1.conf -s /run/field-r9/u1.ctl -P /run/field-r9/u1.pid')
time.sleep(25)
event('hard-reset-observed')
bird_r1('show protocols')

# --- phase 2: carrier-up silent blackhole of U1 (BGP + forwarding) ---
event('blackhole-start')
g("nft add table inet r9bh; nft 'add chain inet r9bh forward { type filter hook forward priority -200; policy accept; }; "
  "add chain inet r9bh input { type filter hook input priority -200; policy accept; }; "
  "add rule inet r9bh forward ip saddr 10.255.0.3 drop; add rule inet r9bh forward ip daddr 10.255.0.3 drop; "
  "add rule inet r9bh input ip saddr 10.255.0.3 drop")
bh_start = time.time()
# poll r1 until the U1 session leaves Established (hold-timer expiry)
detect = None
for _ in range(150):
    p = bird_r1('show protocols')
    m = [l for l in p.stdout.splitlines() if l.startswith('peer_field_u1')]
    if m and 'Established' not in m[0]:
        detect = time.time() - bh_start
        break
    time.sleep(2)
event('blackhole-session-down', f'detect_s={detect}')
time.sleep(10)
g('nft delete table inet r9bh')
event('blackhole-end')
time.sleep(20)

# --- phase 3: U1 flaps every 5 s for 5 min ---
event('flap-start')
for i in range(60):
    t = time.monotonic()
    if i % 2 == 0:
        g('birdc -s /run/field-r9/u1.ctl disable dut')
    else:
        g('birdc -s /run/field-r9/u1.ctl enable dut')
    event(f'flap-{i}')
    time.sleep(max(0, 5 - (time.monotonic() - t)))
g('birdc -s /run/field-r9/u1.ctl enable dut')
event('flap-stop')
time.sleep(30)
bird_r1('show protocols')
event('settled')

# --- phase 4: bird restart on r1, FIB retention ---
p = bird_r1('show route protocol bgp')
fib_before = p.stdout
run(['python3', '/work/field/console.py', 'r1',
     'busybox killall -9 bird; sleep 1; busybox ls /run/kuberoot/router/', '--timeout', '20'])
time.sleep(2)
p = run(['python3', '/work/field/console.py', 'r1',
         'ip route show proto bgp 2>/dev/null | busybox wc -l; busybox cat /proc/net/route | busybox wc -l', '--timeout', '20'])
event('bird-killed', p.stdout.strip()[-200:])
for _ in range(30):
    p = bird_r1('show protocols')
    if 'peer_field_u1' in p.stdout and 'Established' in p.stdout:
        break
    time.sleep(2)
event('bird-restarted')
bird_r1('show protocols')

# --- collect ---
run(['/work/field/guest-command', 'probe1', 'pkill fping; sleep 1; cat /tmp/R9-probes.txt'], timeout=30)
probes = rows[-1]['stdout']
(raw / 'R9-kuberoot-probes.txt').write_text(probes)
summary = {'timers': 'not configurable via BGPPeer API; rendered BIRD defaults', 'bfd': 'not present in BGPPeer API',
           'blackhole_detect_s': detect, 'events': len(events)}
(raw / 'R9-kuberoot.summary.json').write_text(json.dumps(summary, indent=2) + '\n')
(raw / 'R9-kuberoot.json').write_text(json.dumps(rows, indent=2) + '\n')
k(['delete', 'bgppeers', 'field-u1', 'field-u2'])
p = k(['get', 'safeguard', 'default', '-o', 'json'])
s = json.loads(p.stdout)['status']
k(['patch', 'safeguard', 'default', '--type=merge', '-p',
   json.dumps({'spec': {'confirmWithin': '60s', 'confirm': s.get('pending') or s['running']}})])
(raw / 'R9-kuberoot.exit').write_text('0\n')
print(json.dumps(summary), flush=True)
