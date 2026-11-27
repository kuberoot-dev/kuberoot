#!/usr/bin/env python3
"""R4 (remaining cases) for kuberoot r2 on topology G, all through the router API.

A  static host inside the pool, then the pool filled by 100 dynamic clients
B  release 20 leases, 20 new clients retry (reuse <= 10 s)
C  employee (eth3) and guest (VLAN 20 on eth3) zones: guest gets only its own
   pool, reaches the router and the WAN, never the employee LAN
D  two MACs sending the same DHCP client ID
DHCP clients are scapy (dhcp-scapy.py) on the client VM, guest side in a netns.
"""
import base64, json, os, subprocess, sys, time
from pathlib import Path

PH = sys.argv[1] if len(sys.argv) > 1 else 'ABCD'
DUT = os.environ.get('FIELD_DUT', 'r2'); RUN = DUT + ('-' + os.environ['FIELD_TAG'] if os.environ.get('FIELD_TAG') else '')
raw = Path('/work/field/raw'); env = dict(os.environ, KUBECONFIG=f'/work/cluster-{DUT}.conf'); out = {}


def say(*a):
    print(time.strftime('%H:%M:%S', time.gmtime()), *a, flush=True)


def kubectl(a, data=None, quiet=False):
    if not quiet:
        say('$ kubectl', ' '.join(a) + (' < generated' if data else ''))
    p = subprocess.run(['kubectl', '--request-timeout=20s'] + a, env=env, input=data, capture_output=True, text=True, timeout=60)
    if not quiet and (p.stdout + p.stderr).strip():
        print((p.stdout + p.stderr).strip()[-500:], flush=True)
    return p


def guest(vm, cmd, quiet=False, data=None, timeout=120):
    if not quiet:
        say('$ guest-command', vm, cmd[:150])
    p = subprocess.run(['/work/field/guest-command', vm, cmd], input=data, capture_output=True, text=True, timeout=timeout)
    if not quiet and (p.stdout + p.stderr).strip():
        print((p.stdout + p.stderr).strip()[-400:], flush=True)
    return p


def obj(kind, name, spec):
    return {'apiVersion': 'router.kuberoot.dev/v1alpha1', 'kind': kind, 'metadata': {'name': name}, 'spec': spec}


def apply(*objs):
    return kubectl(['apply', '-f', '-'], json.dumps({'apiVersion': 'v1', 'kind': 'List', 'items': list(objs)}))


def leases(name):
    p = kubectl(['get', 'dhcpserver', name, '-o', 'json'], quiet=True)
    return json.loads(p.stdout).get('status', {}).get('leases', []) if p.returncode == 0 else None


def dora(tag, clients, ns=None, iface='test0', timeout=40):
    guest('client', f'cat >/tmp/{tag}.json', data=json.dumps(clients), quiet=True)
    pre = f'ip netns exec {ns} ' if ns else ''
    say(f'$ guest-command client {pre}python3 /tmp/dhcp-scapy.py dora {iface} /tmp/{tag}.json  # {len(clients)} clients')
    p = guest('client', f'{pre}python3 /tmp/dhcp-scapy.py dora {iface} /tmp/{tag}.json --timeout {timeout}', quiet=True, timeout=timeout + 30)
    try:
        r = json.loads(p.stdout)
    except Exception:
        r = {'error': p.stdout[-500:] + p.stderr[-500:]}
    (raw / f'R4-{RUN}-{tag}.json').write_text(json.dumps(r, indent=2) + '\n')
    say(tag, json.dumps({k: v for k, v in r.items() if k != 'results'}))
    return r


def macs(prefix, n, start=0):
    return [{'mac': f'{prefix}:{(i >> 8) & 255:02x}:{i & 255:02x}', 'cid': None} for i in range(start, start + n)]


code = base64.b64encode(Path('/work/field/dhcp-scapy.py').read_bytes()).decode()
guest('client', f'echo {code} | base64 -d >/tmp/dhcp-scapy.py', quiet=True)
EMP = {'link': 'eth3', 'rangeStart': '192.168.10.100', 'rangeEnd': '192.168.10.199', 'leaseTime': '1h', 'gateway': '192.168.10.1',
       'dnsServers': ['192.168.10.1'], 'staticLeases': [{'mac': '02:44:00:00:00:fe', 'address': '192.168.10.150', 'hostname': 'printer'}]}

if 'A' in PH:
    # --- A: static host inside the pool, pool filled ---
    kubectl(['delete', 'dhcpserver', 'g-lan', '--ignore-not-found']); time.sleep(5)
    apply(obj('DHCPServer', 'g-lan', EMP)); time.sleep(8)
    dyn = dora('A-fill100', macs('02:44:00:01', 100), timeout=240)
    static = dora('A-static', [{'mac': '02:44:00:00:00:fe', 'cid': None}])
    extra = dora('A-exhausted', macs('02:44:00:02', 1), timeout=15)
    acked = [s['ack'] for s in dyn.get('results', []) if s['ack']]
    st = leases('g-lan') or []
    out['A'] = {'dynamic_acked': len(acked), 'dynamic_distinct': len(set(acked)), 'dynamic_got_static_address': '192.168.10.150' in acked,
                'static_ack': static.get('results', [{}])[0].get('ack'), 'extra_client_ack': extra.get('results', [{}])[0].get('ack'),
                'status_leases': len(st), 'status_duplicate_addresses': len(st) - len({l['address'] for l in st})}
    say('A', json.dumps(out['A']))

    # --- B: release 20, retry with 20 new clients ---
    rel = [{'mac': s['mac'], 'address': s['ack'], 'cid': None} for s in dyn.get('results', []) if s['ack']][:20]
    guest('client', 'cat >/tmp/R4-release.json', data=json.dumps(rel), quiet=True)
    guest('client', 'python3 /tmp/dhcp-scapy.py release test0 /tmp/R4-release.json 192.168.10.1')
    time.sleep(3)
    after_release = leases('g-lan') or []
    retry = dora('B-retry20', macs('02:44:00:03', 20), timeout=90)
    racked = [s['ack'] for s in retry.get('results', []) if s['ack']]
    st = leases('g-lan') or []
    out['B'] = {'released': len(rel), 'status_leases_before': out['A']['status_leases'], 'status_leases_after_release': len(after_release),
                'retry_acked': len(racked), 'retry_last_ack_s': retry.get('last_ack_s'), 'retry_got_released_addresses': len(set(racked) & {r['address'] for r in rel}),
                'status_leases_after_retry': len(st), 'status_duplicate_addresses': len(st) - len({l['address'] for l in st})}
    say('B', json.dumps(out['B']))

if 'C' in PH:
    # --- C: guest VLAN isolation ---
    apply(obj('Interface', 'g-guest', {'link': 'eth3.20', 'vlan': {'parent': 'eth3', 'id': 20}, 'addresses': ['192.168.30.1/24']}),
          obj('FirewallZone', 'g-guest', {'links': ['eth3.20'], 'input': 'Accept', 'forwardTo': ['g-wan']}),
          obj('NATRule', 'g-masq-guest', {'masquerade': {'outLink': 'eth4', 'sources': ['192.168.30.0/24']}}),
          obj('DHCPServer', 'g-guest', {'link': 'eth3.20', 'rangeStart': '192.168.30.100', 'rangeEnd': '192.168.30.199', 'leaseTime': '1h'}))
    time.sleep(10)
    kubectl(['get', 'interfaces,firewallzones,natrules,dhcpservers', '-o', 'wide'])
    guest('client', 'ip netns del guest20 2>/dev/null; sleep 2; ip netns add guest20; ip link add link test0 name guest20 type vlan id 20; ip link set guest20 netns guest20; '
          'ip netns exec guest20 ip link set lo up; ip netns exec guest20 ip link set guest20 address 02:45:00:00:00:01; ip netns exec guest20 ip link set guest20 up && ip netns exec guest20 ip -br link show guest20')
    time.sleep(2)
    g = dora('C-guest-dora', [{'mac': '02:45:00:00:00:01', 'cid': None}] + macs('02:45:00:01', 10), ns='guest20', iface='guest20', timeout=90)
    gacked = [s['ack'] for s in g.get('results', []) if s['ack']]
    mine = g.get('results', [{}])[0].get('ack')
    cases = {}
    if mine:
        guest('client', f'ip netns exec guest20 ip addr add {mine}/24 dev guest20; ip netns exec guest20 ip route add default via 192.168.30.1')
        guest('client', 'tcpdump -U -n -i test0 -w /tmp/R4-leak.pcap "src net 192.168.30.0/24" >/dev/null 2>&1 & echo $! >/tmp/R4-leak.pid', quiet=True)
        time.sleep(1)
        for name, cmd in [('guest-to-router', 'ping -c 20 -i .1 -W 1 192.168.30.1'), ('guest-to-wan', 'ping -c 20 -i .1 -W 1 198.19.0.1'),
                          ('guest-to-employee-host-icmp', 'ping -c 20 -i .1 -W 1 192.168.10.2'), ('guest-to-employee-host-tcp22', 'nc -z -w 3 192.168.10.2 22'),
                          ('guest-to-router-employee-address', 'ping -c 5 -i .2 -W 1 192.168.10.1')]:
            p = guest('client', 'ip netns exec guest20 ' + cmd)
            cases[name] = {'exit': p.returncode, 'tail': (p.stdout + p.stderr).strip().splitlines()[-2:]}
        guest('client', 'kill -INT $(cat /tmp/R4-leak.pid); sleep 1', quiet=True)
        leak = guest('client', 'tcpdump -n -r /tmp/R4-leak.pcap 2>/dev/null | wc -l', quiet=True).stdout.strip()
        cases['packets_from_guest_net_seen_on_employee_lan'] = int(leak or 0)
    out['C'] = {'guest_acked': len(gacked), 'guest_all_in_guest_pool': all(a.startswith('192.168.30.') for a in gacked), 'cases': cases}
    say('C', json.dumps(out['C'])[:900])

if 'D' in PH:
    # --- D: duplicate client IDs (guest pool) ---
    cid = 'ff00a1b2c3d4'
    d1 = dora('D-cid-first', [{'mac': '02:46:00:00:00:01', 'cid': cid}], ns='guest20', iface='guest20', timeout=15)
    d2 = dora('D-cid-second', [{'mac': '02:46:00:00:00:02', 'cid': cid}], ns='guest20', iface='guest20', timeout=15)
    a1 = d1.get('results', [{}])[0].get('ack'); a2 = d2.get('results', [{}])[0].get('ack')
    st = leases('g-guest') or []
    out['D'] = {'first_mac_ack': a1, 'second_mac_ack': a2, 'same_address_to_two_macs': bool(a1 and a1 == a2),
                'status_leases_for_these_macs': [l for l in st if l['mac'].startswith('02:46')]}
    say('D', json.dumps(out['D']))

(raw / f'R4-{RUN}-extra-{PH}.summary.json').write_text(json.dumps(out, indent=2) + '\n')
# --- restore ---
guest('client', 'ip netns del guest20; true', quiet=True)
kubectl(['delete', 'dhcpserver', 'g-guest', '--ignore-not-found'], quiet=True); kubectl(['delete', 'natrule', 'g-masq-guest', '--ignore-not-found'], quiet=True)
kubectl(['delete', 'firewallzone', 'g-guest', '--ignore-not-found'], quiet=True); kubectl(['delete', 'interface', 'g-guest', '--ignore-not-found'], quiet=True)
kubectl(['delete', 'dhcpserver', 'g-lan', '--ignore-not-found'], quiet=True)
say('done')
