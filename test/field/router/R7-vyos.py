#!/usr/bin/env python3
"""R7 for VyOS: two BIRD upstreams, FRR RIB/FIB and sampled forwarding.

Mirrors R7-debian.py but drives the VyOS DUT: interfaces via the native VyOS
CLI, BGP via vtysh on the integrated FRR. Generators, probes and pass criteria
are identical so the products compare directly.
"""
import json, subprocess, time, ipaddress, os
from pathlib import Path

raw = Path('/work/field/raw')
ram_label = os.environ.get('FIELD_DUT_RAM_GIB', '2') + 'GiB'

def dut(cmd, timeout=90):
    print(time.strftime('%FT%TZ', time.gmtime()), '$ vyos-command', cmd, flush=True)
    p = subprocess.run(['/work/field/vyos-command', cmd], capture_output=True, text=True, timeout=timeout)
    print(p.stdout + p.stderr, flush=True)
    return p

def guest(vm, cmd, timeout=90):
    print(time.strftime('%FT%TZ', time.gmtime()), '$ guest-command', vm, cmd, flush=True)
    p = subprocess.run(['/work/field/guest-command', vm, cmd], capture_output=True, text=True, timeout=timeout)
    print(p.stdout + p.stderr, flush=True)
    return p

def put(vm, path, data):
    subprocess.run(['/work/field/guest-command', vm, 'cat >' + path], input=data, text=True, check=True)

def vtysh(script):
    # script: list of vtysh -c arguments
    args = ' '.join(f"-c '{c}'" for c in script)
    return dut(f"vtysh {args}")

while not (raw / 'vyos-fabric-setup.exit').exists():
    time.sleep(3)
guest('client', 'ip route replace 10.64.0.0/12 via 192.168.10.1')
# second upstream VLAN on the WAN interface, native CLI
dut("configure; set interfaces ethernet eth0 vif 20 address 198.18.20.1/24; commit; exit")
dut("ip -6 addr add fd42:1::1/64 dev eth0 2>/dev/null; ip -6 addr add fd42:2::1/64 dev eth0.20 2>/dev/null; true")
guest('gateway', 'systemctl stop bird; ip netns add upstream2 2>/dev/null; ip link add link test0 name upstream2 type vlan id 20 2>/dev/null; ip link set upstream2 netns upstream2 2>/dev/null; ip netns exec upstream2 ip link set lo up; ip netns exec upstream2 ip addr add 198.18.20.2/24 dev upstream2 2>/dev/null; ip netns exec upstream2 ip link set upstream2 up; ip netns exec upstream2 ip route add default via 198.18.20.1 2>/dev/null; ip -6 addr add fd42:1::2/64 dev test0 2>/dev/null; ip netns exec upstream2 ip -6 addr add fd42:2::2/64 dev upstream2 2>/dev/null; mkdir -p /run/field-bird')
# enable bgpd via native CLI, then peers via vtysh (same FRR as the debian leg)
dut("configure; set protocols bgp system-as 65000; set protocols bgp router-id 198.18.0.1; commit; exit")
vtysh(['configure terminal', 'router bgp 65000', 'no bgp ebgp-requires-policy', 'no bgp network import-check',
       'neighbor 198.18.0.2 remote-as 65001', 'neighbor 198.18.0.2 timers 3 9',
       'neighbor 198.18.20.2 remote-as 65002', 'neighbor 198.18.20.2 timers 3 9',
       'address-family ipv4 unicast', 'neighbor 198.18.0.2 activate', 'neighbor 198.18.20.2 activate',
       'exit-address-family'])

for count in [int(x) for x in os.environ.get('FIELD_PREFIX_COUNTS', '10000,100000').split(',')]:
    ipv6 = int(os.environ.get('FIELD_IPV6_COUNT', '0'))
    for side, local in [(1, '198.18.0.2'), (2, '198.18.20.2')]:
        path = f'/work/field/upstream-vyos-{side}.conf'
        subprocess.run(['python3', '/work/field/bgp-prefix-generator.py', path, '--ipv4', str(count), '--ipv6', str(ipv6),
                        '--asn', str(65000 + side), '--local', local, '--peer', '198.18.0.1' if side == 1 else '198.18.20.1'], check=True)
        put('gateway', f'/tmp/upstream-vyos-{side}.conf', Path(path).read_text())
        prefix = '' if side == 1 else 'ip netns exec upstream2 '
        guest('gateway', f'if test -e /run/field-bird/vyos{side}.pid; then kill $(cat /run/field-bird/vyos{side}.pid); sleep 1; fi; {prefix}bird -c /tmp/upstream-vyos-{side}.conf -s /run/field-bird/vyos{side}.ctl -P /run/field-bird/vyos{side}.pid')
    start = time.monotonic(); rows = []
    while time.monotonic() - start < 300:
        t = time.monotonic()
        p = dut("vtysh -c 'show bgp ipv4 unicast summary json'; ip -4 route show proto bgp | wc -l; head -3 /proc/meminfo", 30)
        rows.append({'elapsed_s': time.monotonic() - start, 'observer_ssh_s': time.monotonic() - t, 'exit': p.returncode, 'text': p.stdout, 'stderr': p.stderr})
        lines = p.stdout.strip().splitlines()
        fib = int(lines[-4]) if len(lines) >= 4 and lines[-4].isdigit() else None
        try:
            peers = json.JSONDecoder().raw_decode(p.stdout.lstrip())[0]['peers']
            both = all(peers.get(ip, {}).get('pfxRcd') == count and peers[ip].get('state') == 'Established' for ip in ['198.18.0.2', '198.18.20.2'])
        except (ValueError, KeyError):
            both = False
        if fib == count and both:
            break
        time.sleep(3)
    (raw / f'R7-vyos-{count}-{ram_label}.json').write_text(json.dumps(rows, indent=2) + '\n')
    addresses = [str(ipaddress.IPv4Address(int(ipaddress.IPv4Address('10.64.0.0')) + i * (count // 1000))) for i in range(1000)]
    for side in [1, 2]:
        prefix = '' if side == 1 else 'ip netns exec upstream2 '
        put('gateway', f'/tmp/R7-vyos-samples-{side}.sh', '\n'.join(prefix + 'ip addr replace ' + a + '/32 dev lo' for a in addresses) + '\n')
        guest('gateway', f'sh /tmp/R7-vyos-samples-{side}.sh')
    put('client', '/tmp/R7-samples.txt', '\n'.join(addresses) + '\n')
    p = guest('client', 'fping -c 1 -t 1000 -i 2 -f /tmp/R7-samples.txt', 60)
    (raw / f'R7-vyos-{count}-samples-{ram_label}.txt').write_text(p.stdout + p.stderr)

(raw / ('R7-vyos-full' + os.environ.get('FIELD_DUT_RAM_GIB', '2') + '.exit' if os.environ.get('FIELD_IPV6_COUNT') else 'R7-vyos-small.exit')).write_text('0\n')
