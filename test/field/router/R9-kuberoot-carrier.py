#!/usr/bin/env python3
"""R9 (remaining phases) for kuberoot r2 on topology G: carrier down/up and a
BIRD restart on the DUT, with a second upstream on a different link.

U1: BIRD on the gateway, 198.18.0.2 AS65001, reached over r2 eth4 (G WAN).
U2: the same BIRD, 10.255.0.4 AS65003, reached over r2 eth2 (transit).
Both announce 10.64.0.0/24 (10.64.0.0 is a gateway loopback address); the
gateway learns 192.168.10.0/24 back from r2 over both sessions, so the return
path fails over too. The client pings 10.64.0.0 every 100 ms through r2.

Phases: carrier down on both ends of the U1 cable (QMP set_link on r2 gwan and
the gateway's site NIC) for 30 s, then up; restart of kuberoot-router through
NodeService.spec.restartedAt (bird is its child, so this restarts bird; the
BGP API has no option to keep the FIB across a restart).
"""
import json, os, subprocess, time
from pathlib import Path

raw = Path('/work/field/raw'); env = dict(os.environ, KUBECONFIG='/work/cluster-r2.conf')
node_env = dict(os.environ, KUBECONFIG='/work/trust/admin.kubeconfig')
events = []


def say(*a):
    print(time.strftime('%H:%M:%S', time.gmtime()), *a, flush=True)


def kubectl(a, data=None, quiet=False, node=False, timeout=30):
    cmd = ['kubectl', '--request-timeout=10s'] + (['--server', 'https://127.0.0.1:5020'] if node else []) + a
    if not quiet:
        say('$ kubectl', ' '.join(a) + (' < generated' if data else ''))
    try:
        p = subprocess.run(cmd, env=node_env if node else env, input=data, capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        p = subprocess.CompletedProcess(cmd, 124, '', 'timeout')
    if not quiet and (p.stdout + p.stderr).strip():
        print((p.stdout + p.stderr).strip()[-600:], flush=True)
    return p


def guest(vm, cmd, quiet=False, data=None, timeout=60):
    if not quiet:
        say('$ guest-command', vm, cmd[:160])
    p = subprocess.run(['/work/field/guest-command', vm, cmd], input=data, capture_output=True, text=True, timeout=timeout)
    if not quiet and (p.stdout + p.stderr).strip():
        print((p.stdout + p.stderr).strip()[-600:], flush=True)
    return p


def qmp(vm, cmd, args):
    say(f'$ qmp {vm} {cmd} {json.dumps(args)}')
    subprocess.run(['python3', '/work/field/qmp.py', vm, cmd, json.dumps(args)], check=True, capture_output=True)


def obj(kind, name, spec):
    return {'apiVersion': 'router.kuberoot.dev/v1alpha1', 'kind': kind, 'metadata': {'name': name}, 'spec': spec}


def apply(*objs):
    return kubectl(['apply', '-f', '-'], json.dumps({'apiVersion': 'v1', 'kind': 'List', 'items': list(objs)}))


def peers():
    p = kubectl(['get', 'bgppeers', '-o', 'json'], quiet=True)
    try:
        return {i['metadata']['name']: (i.get('status', {}).get('state'), i.get('status', {}).get('routesReceived')) for i in json.loads(p.stdout)['items']}
    except Exception:
        return {}


def ev(phase, **kw):
    events.append({'at': time.time(), 'phase': phase, **kw})
    (raw / 'R9-r2-carrier-events.json').write_text(json.dumps(events, indent=2) + '\n')


def watch(seconds, names=('g-u1', 'g-u2t'), tag=''):
    """Poll peer states; record transitions with time since the phase start."""
    t0 = time.monotonic(); last = None; trans = []
    while time.monotonic() - t0 < seconds:
        st = peers(); cur = tuple(st.get(n) for n in names)
        if cur != last:
            trans.append({'t': round(time.monotonic() - t0, 2), 'peers': dict(zip(names, cur))})
            say(f'{tag} +{time.monotonic() - t0:5.1f}s', ' '.join(f'{n}={s}' for n, s in zip(names, cur)))
            last = cur
        time.sleep(1)
    return trans


BIRD = '''log stderr all;
router id 198.18.0.2;
protocol device {}
protocol static ann { ipv4; route 10.64.0.0/24 blackhole; }
protocol kernel { ipv4 { import none; export where source = RTS_BGP; }; }
# U1 is the primary path both ways: U2 prepends its AS path, the gateway prefers
# the return route learned over U1. Short error-wait timers on the upstream so
# reconnection time is the DUT's, not the upstream's back-off.
protocol bgp u1 { local 198.18.0.2 as 65001; neighbor 198.18.0.1 as 65002; hold time 9; keepalive time 3; error wait time 1, 5;
  ipv4 { import filter { if net = 192.168.10.0/24 then { bgp_local_pref = 200; accept; } reject; };
         export where source = RTS_STATIC; }; }
protocol bgp u2 { local 10.255.0.4 as 65003; neighbor 10.255.0.2 as 65002; hold time 9; keepalive time 3; error wait time 1, 5;
  ipv4 { import where net = 192.168.10.0/24;
         export filter { if source = RTS_STATIC then { bgp_path.prepend(65003); bgp_path.prepend(65003); accept; } reject; }; }; }
'''

# --- setup ---
guest('gateway', 'pkill bird; sleep 1; ip route del 192.168.10.0/24 via 198.18.0.1 2>/dev/null; mkdir -p /run/field-r9c; true')
guest('gateway', 'cat >/root/R9-carrier.conf', data=BIRD, quiet=True)
guest('gateway', 'bird -c /root/R9-carrier.conf -s /run/field-r9c/bird.ctl -P /run/field-r9c/bird.pid')
kubectl(['delete', 'bgppeer', 'g-u1', 'g-u2', 'g-u1v6', 'g-u2v6', 'g-u2t', '--ignore-not-found'], quiet=True)
kubectl(['delete', 'interface', 'g-wan20', '--ignore-not-found'], quiet=True)
apply(obj('BGPRouter', 'default', {'asn': 65002, 'routerID': '10.255.0.2', 'import': 'All', 'announce': ['192.168.20.0/24', '192.168.10.0/24']}),
      obj('BGPPeer', 'g-u1', {'address': '198.18.0.2', 'asn': 65001}),
      obj('BGPPeer', 'g-u2t', {'address': '10.255.0.4', 'asn': 65003}),
      obj('FirewallZone', 'g-lan', {'links': ['eth3'], 'input': 'Accept', 'forwardTo': ['g-wan', 'transit']}))
guest('client', 'ip route replace 10.64.0.0/10 via 192.168.10.1', quiet=True)
for _ in range(60):
    st = peers()
    if st.get('g-u1', (None,))[0] == 'Established' and st.get('g-u2t', (None,))[0] == 'Established':
        break
    time.sleep(2)
say('peers', json.dumps(peers()))
guest('gateway', 'birdc -s /run/field-r9c/bird.ctl show protocols; ip route show 192.168.10.0/24')
config = kubectl(['get', 'bgprouter,bgppeers', '-o', 'yaml'], quiet=True).stdout
(raw / 'R9-r2-carrier-config.yaml').write_text(config)
skew = float(guest('client', 'date +%s.%N', quiet=True).stdout) - time.time()
routes = [r for r in json.loads(kubectl(['get', 'bgprouter', 'default', '-o', 'json'], quiet=True).stdout)['status'].get('routes', []) if r['destination'] == '10.64.0.0/24']
gw = guest('gateway', 'ip route show 192.168.10.0/24', quiet=True).stdout.strip()
say('DUT route to 10.64.0.0/24:', json.dumps(routes), '| gateway return route:', gw)
ev('setup', peers=peers(), client_clock_minus_pod_s=skew, dut_routes=routes, gateway_return_route=gw)
guest('client', 'rm -f /tmp/R9c.txt; nohup timeout 600 fping -D -l -p 100 10.64.0.0 >/tmp/R9c.txt 2>&1 </dev/null &')
time.sleep(20)

# --- carrier down on both ends of the U1 cable ---
ev('carrier-down')
qmp('r2', 'set_link', {'name': 'gwan', 'up': False}); qmp('gateway', 'set_link', {'name': 'site', 'up': False})
down = watch(30, tag='carrier down')
ev('carrier-up', transitions=down)
qmp('r2', 'set_link', {'name': 'gwan', 'up': True}); qmp('gateway', 'set_link', {'name': 'site', 'up': True})
up = watch(60, tag='carrier up')
ev('carrier-up-observed', transitions=up)

# --- restart kuberoot-router (and with it bird) on the DUT ---
before = kubectl(['get', 'nodeservice', 'kuberoot-router', '-o', 'json'], node=True, quiet=True).stdout
now = time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())
ev('router-restart', nodeservice_before=json.loads(before).get('status') if before else None)
kubectl(['patch', 'nodeservice', 'kuberoot-router', '--type=merge', '-p', json.dumps({'spec': {'restartedAt': now}})], node=True)
rs = watch(90, tag='bird restart')
after = kubectl(['get', 'nodeservice', 'kuberoot-router', '-o', 'json'], node=True, quiet=True).stdout
ev('router-restart-observed', transitions=rs, nodeservice_after=json.loads(after).get('status') if after else None)
time.sleep(10)

# --- collect and analyse probes ---
guest('client', 'pkill fping; sleep 1', quiet=True)
data = guest('client', 'cat /tmp/R9c.txt', quiet=True).stdout
(raw / 'R9-r2-carrier-probes.txt').write_text(data)
replies = []
for line in data.splitlines():
    if line.startswith('[') and 'bytes' in line:
        replies.append(float(line[1:line.index(']')]) - skew)
gaps = [(a, b - a) for a, b in zip(replies, replies[1:])]
starts = {e['phase']: e['at'] for e in events}
windows = {'carrier': (starts['carrier-down'], starts['carrier-up-observed']),
           'router-restart': (starts['router-restart'], starts['router-restart-observed'])}
summary = {'replies': len(replies), 'span_s': replies[-1] - replies[0] if replies else None}
for name, (a, b) in windows.items():
    g = sorted(d for t, d in gaps if a - 1 <= t <= b)
    summary[name] = {'max_gap_s': round(g[-1], 3) if g else None, 'gaps_over_0_3s': [round(x, 3) for x in g if x > .3],
                     'outage_from_event_s': next((round(t + d - a, 3) for t, d in gaps if a - 1 <= t <= b and d > .3), 0.0)}
summary['carrier_transitions'] = {'down': down, 'up': up}; summary['restart_transitions'] = rs
(raw / 'R9-r2-carrier.summary.json').write_text(json.dumps(summary, indent=2) + '\n')
say('carrier:', json.dumps(summary['carrier']))
say('router restart:', json.dumps(summary['router-restart']))

# --- restore ---
guest('gateway', 'pkill bird; ip route replace 192.168.10.0/24 via 198.18.0.1 dev test0; true', quiet=True)
kubectl(['delete', 'bgppeer', 'g-u1', 'g-u2t', '--ignore-not-found'], quiet=True)
apply(obj('BGPRouter', 'default', {'asn': 65002, 'routerID': '10.255.0.2', 'import': 'All', 'announce': ['192.168.20.0/24']}),
      obj('FirewallZone', 'g-lan', {'links': ['eth3'], 'input': 'Accept', 'forwardTo': ['g-wan']}))
