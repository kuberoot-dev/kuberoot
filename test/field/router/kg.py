#!/usr/bin/env python3
"""kuberoot r2 as the DUT on topology G (same client/gateway/sink as the competitors).

usage: kg.py attach | detach | profile routing|nat | r4 | r5 | r7 COUNT[,COUNT] [IPV6] | r9
attach restarts r2's QEMU with two more NICs on the G segments (LAN 13031 -> eth3,
WAN 13032 -> eth4) and configures them through the router API. The competitor
slot must be empty: the G addresses are the same for every DUT.
"""
import base64, ipaddress, json, os, subprocess, sys, time
from pathlib import Path

raw = Path('/work/field/raw'); KC = '/work/cluster-r2.conf'; env = dict(os.environ, KUBECONFIG=KC)
row, args = sys.argv[1], sys.argv[2:]


def say(*a):
    print(time.strftime('%H:%M:%S', time.gmtime()), *a, flush=True)


def kubectl(a, data=None, quiet=False, timeout=60):
    if not quiet:
        say('$ kubectl', ' '.join(a) + (' < generated' if data else ''))
    try:
        p = subprocess.run(['kubectl', '--request-timeout=20s'] + a, env=env, input=data, capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        p = subprocess.CompletedProcess([], 124, '', 'timeout')
    if not quiet and (p.stdout + p.stderr).strip():
        print((p.stdout + p.stderr).strip()[-900:], flush=True)
    return p


def guest(vm, cmd, timeout=120, quiet=False):
    if not quiet:
        say('$ guest-command', vm, cmd[:200])
    p = subprocess.run(['/work/field/guest-command', vm, cmd], capture_output=True, text=True, timeout=timeout)
    if not quiet and (p.stdout + p.stderr).strip():
        print((p.stdout + p.stderr).strip()[-1200:], flush=True)
    return p


def put(vm, path, data):
    subprocess.run(['/work/field/guest-command', vm, 'cat >' + path], input=data, text=True, check=True)


def obj(kind, name, spec):
    return {'apiVersion': 'router.kuberoot.dev/v1alpha1', 'kind': kind, 'metadata': {'name': name}, 'spec': spec}


def apply(*objs):
    return kubectl(['apply', '-f', '-'], json.dumps({'apiVersion': 'v1', 'kind': 'List', 'items': list(objs)}))


def save(name, o):
    (raw / name).write_text(json.dumps(o, indent=2) + '\n'); say('saved raw/' + name)


def ping_ok():
    return guest('client', 'ping -c1 -W1 198.19.0.1 >/dev/null', quiet=True).returncode == 0


def wait_api(limit=300):
    t = time.monotonic()
    while time.monotonic() - t < limit:
        if kubectl(['get', '--raw', '/readyz'], quiet=True, timeout=10).returncode == 0:
            return time.monotonic() - t
        time.sleep(2)


def r2_argv():
    pid = Path('/work/vms/r2/pid').read_text().strip()
    return [x.decode() for x in Path(f'/proc/{pid}/cmdline').read_bytes().split(b'\0') if x]


def restart(argv):
    pid = int(Path('/work/vms/r2/pid').read_text())
    subprocess.run(['python3', '/work/field/qmp.py', 'r2', 'system_powerdown'], capture_output=True)
    for _ in range(600):
        if not Path(f'/proc/{pid}').exists():
            break
        time.sleep(.1)
    else:
        subprocess.run(['python3', '/work/field/qmp.py', 'r2', 'quit'], capture_output=True); time.sleep(2)
    Path('/work/vms/r2/pid').unlink(missing_ok=True)
    for _ in range(20):
        if subprocess.run(argv, capture_output=True).returncode == 0:
            return
        time.sleep(1)
    raise SystemExit('r2 restart failed')


G = ['-netdev', 'socket,id=glan,mcast=230.31.0.31:13031,localaddr=127.0.0.1', '-device', 'virtio-net-pci,romfile=,netdev=glan,mac=52:54:00:30:00:12,addr=0x6',
     '-netdev', 'socket,id=gwan,mcast=230.31.0.32:13032,localaddr=127.0.0.1', '-device', 'virtio-net-pci,romfile=,netdev=gwan,mac=52:54:00:30:00:11,addr=0x7']


def pin_disk(argv):
    # `-drive if=virtio` gets the first free PCI slot after the explicit devices (0x5 here);
    # the UEFI boot entries of both kuberoot slots name that path, so keep the disk at 0x5.
    out = []
    for a in argv:
        if a.startswith('file=/work/vms/r2/sys.qcow2') and 'if=virtio' in a:
            out.append(a.replace('if=virtio', 'if=none,id=sysdisk'))
            out += ['-device', 'virtio-blk-pci,drive=sysdisk,addr=0x5']
            continue
        out.append(a)
    return out


def row_attach():
    for other in ('vyos', 'openwrt', 'debian'):
        f = Path(f'/work/vms/{other}/pid')
        if f.exists() and Path('/proc/' + f.read_text().strip()).exists():
            raise SystemExit(f'competitor {other} is running; G addresses are taken')
    base = Path('/work/vms/r2/launch-base.json')
    argv = json.loads(base.read_text())['argv'] if base.exists() else r2_argv()
    if 'glan' in ' '.join(r2_argv()) and 'sysdisk' in ' '.join(r2_argv()):
        say('r2 already attached to G')
    else:
        ram = os.environ.get('R2_RAM_MIB')
        if ram:
            argv[argv.index('-m') + 1] = ram
        Path('/work/vms/r2/launch-base.json').write_text(json.dumps({'argv': argv}))
        say('$ restart r2 with two more NICs: G LAN 230.31.0.31:13031 (eth3), G WAN 230.31.0.32:13032 (eth4)')
        restart(pin_disk(argv) + G)
    wait_api(); time.sleep(10)
    apply(obj('Interface', 'g-lan', {'link': 'eth3', 'addresses': ['192.168.10.1/24']}),
          obj('Interface', 'g-wan', {'link': 'eth4', 'addresses': ['198.18.0.1/24']}),
          obj('Route', 'g-sink', {'destination': '198.19.0.0/24', 'gateway': '198.18.0.2', 'link': 'eth4'}),
          obj('FirewallZone', 'g-lan', {'links': ['eth3'], 'input': 'Accept', 'forwardTo': ['g-wan']}),
          obj('FirewallZone', 'g-wan', {'links': ['eth4'], 'input': 'Accept', 'forwardTo': ['g-lan']}))
    for _ in range(60):
        if ping_ok():
            break
        time.sleep(2)
    kubectl(['get', 'interfaces,routes,firewallzones,natrules', '-o', 'wide'])
    say('client -> r2 -> gateway -> sink ping:', ping_ok())


def row_detach():
    base = json.loads(Path('/work/vms/r2/launch-base.json').read_text())['argv']
    kubectl(['delete', 'interface', 'g-lan', 'g-wan', 'g-wan20', '--ignore-not-found'])
    kubectl(['delete', 'route', 'g-sink', '--ignore-not-found']); kubectl(['delete', 'firewallzone', 'g-lan', 'g-wan', '--ignore-not-found'])
    kubectl(['delete', 'natrule', 'g-masq', '--ignore-not-found']); kubectl(['delete', 'dhcpserver', 'g-lan', '--ignore-not-found'])
    kubectl(['delete', 'bgppeer', 'g-u1', 'g-u2', 'g-u1v6', 'g-u2v6', '--ignore-not-found'])
    kubectl(['patch', 'bgprouter', 'default', '--type=merge', '-p', '{"spec":{"asn":65002}}'])
    say('G objects removed; the extra NICs stay (r2 boots its field.6 kernel directly, see launch-directboot.json)')


def row_profile():
    prof = args[0]
    if prof == 'routing':
        kubectl(['delete', 'natrule', 'g-masq', '--ignore-not-found'])
        apply(obj('FirewallZone', 'g-wan', {'links': ['eth4'], 'input': 'Accept', 'forwardTo': ['g-lan']}))
    else:  # nat: masquerade, WAN may not open connections inward; kuberoot's forward chain is always stateful
        apply(obj('NATRule', 'g-masq', {'masquerade': {'outLink': 'eth4', 'sources': ['192.168.10.0/24']}}),
              obj('FirewallZone', 'g-wan', {'links': ['eth4'], 'input': 'Drop', 'forwardTo': []}))
    time.sleep(8)
    kubectl(['get', 'natrules,firewallzones', '-o', 'wide'])
    (raw / f'R-B2-r2-{prof}-state.txt').write_text(kubectl(['get', 'natrules,firewallzones,interfaces,routes', '-o', 'yaml'], quiet=True).stdout)
    say('profile', prof, 'forwarding:', ping_ok())


def row_r4():
    apply(obj('DHCPServer', 'g-lan', {'link': 'eth3', 'rangeStart': '192.168.10.100', 'rangeEnd': '192.168.10.199', 'leaseTime': '1h',
                                     'gateway': '192.168.10.1', 'dnsServers': ['192.168.10.1']}))
    time.sleep(10)
    code = base64.b64encode(Path('/work/field/dhcp-pcap-summary.py').read_bytes()).decode(); guest('client', f'echo {code} | base64 -d >/tmp/dhcp-pcap-summary.py', quiet=True)
    guest('client', 'nohup iperf3 -c 198.19.0.1 -u -b 1M -l 512 -t 500 -J >/tmp/R4-nat-load.json 2>&1 </dev/null &')
    phases = [('cold100', '--scenario avalanche -R 100 -p 30')] + [(f'rate-{r}', f'-r {r} -R 120 -n {r * 30} -p 30') for r in (10, 100, 1000)]
    for phase, options in phases:
        phase = 'mixed-' + phase
        # fresh lease database per phase: recreate the server object (no shell on the node)
        kubectl(['delete', 'dhcpserver', 'g-lan']); time.sleep(5)
        apply(obj('DHCPServer', 'g-lan', {'link': 'eth3', 'rangeStart': '192.168.10.100', 'rangeEnd': '192.168.10.199', 'leaseTime': '1h',
                                         'gateway': '192.168.10.1', 'dnsServers': ['192.168.10.1']})); time.sleep(8)
        guest('client', f'tcpdump -U -n -i test0 -s 0 -w /tmp/R4-r2-{phase}.pcap "udp port 67 or udp port 68" >/tmp/R4-tcpdump.log 2>&1 & echo $! >/tmp/R4-capture.pid')
        time.sleep(1); t0 = time.monotonic()
        p = guest('client', f'timeout --signal=INT --kill-after=3s 35s perfdhcp -4 -B -l test0 -b mac=02:42:00:00:00:00 -s 42 {options} -x aelt > /tmp/R4-r2-{phase}.log 2>&1; code=$?; tail -12 /tmp/R4-r2-{phase}.log; exit $code', timeout=60)
        guest('client', 'kill -INT $(cat /tmp/R4-capture.pid); sleep 1', quiet=True)
        time.sleep(3)
        st = kubectl(['get', 'dhcpserver', 'g-lan', '-o', 'json'], quiet=True)
        status = json.loads(st.stdout).get('status', {}) if st.returncode == 0 else {}
        leases = status.get('leases', [])
        for ext in ['log', 'pcap']:
            q = subprocess.run(['/work/field/guest-command', 'client', f'cat /tmp/R4-r2-{phase}.{ext}'], capture_output=True)
            (raw / f'R4-r2-{phase}.{ext}').write_bytes(q.stdout)
        guest('client', f'python3 /tmp/dhcp-pcap-summary.py /tmp/R4-r2-{phase}.pcap --output /tmp/R4-r2-{phase}.summary.json', quiet=True)
        try:
            summary = json.loads(subprocess.check_output(['/work/field/guest-command', 'client', f'cat /tmp/R4-r2-{phase}.summary.json']))
        except Exception as e:
            summary = {'error': str(e)}
        addrs = [l['address'] for l in leases]
        summary.update(product='kuberoot', phase=phase, perfdhcp_exit=p.returncode, wall_elapsed_s=time.monotonic() - t0,
                       status_active_leases=status.get('activeLeases'), status_duplicate_addresses=len(addrs) - len(set(addrs)))
        save(f'R4-r2-{phase}.summary.json', summary)
        print(json.dumps({k: v for k, v in summary.items() if k != 'leases'})[:700], flush=True)
    before = kubectl(['get', 'dhcpserver', 'g-lan', '-o', 'jsonpath={.status.activeLeases}'], quiet=True).stdout
    say('$ python3 /work/field/qmp.py r2 system_reset  # reboot with leases')
    subprocess.run(['python3', '/work/field/qmp.py', 'r2', 'system_reset'], check=True); time.sleep(10); wait_api(); time.sleep(20)
    after = kubectl(['get', 'dhcpserver', 'g-lan', '-o', 'jsonpath={.status.activeLeases}'], quiet=True).stdout
    save('R4-r2-reboot.json', {'active_leases_before': before, 'active_leases_after': after})
    say('active leases before reboot', before, 'after', after)


def row_r5():
    for vm in ['client', 'sink']:
        code = base64.b64encode(Path('/work/field/tcp-flows.py').read_bytes()).decode(); guest(vm, f'echo {code} | base64 -d >/tmp/tcp-flows.py', quiet=True)
    guest('sink', 'pkill -f "[t]cp-flows.py server"; sleep 3; true')  # separate session: the pattern must not match this shell
    for _ in range(3):
        guest('sink', 'nohup python3 /tmp/tcp-flows.py server --seconds 1500 >/tmp/R5-server.json 2>/tmp/R5-server.err </dev/null & sleep 3')
        if guest('sink', 'ss -ltn | grep -c ":91[0-3][0-9] "').stdout.strip() == '32':
            break
        time.sleep(3)
    else:
        raise SystemExit('flow sink did not listen on 32 ports; no measurement')
    summary = {}
    for count, rate in [(1000, 1000), (10000, 1000), (100000, 10000)]:
        name = f'R5-r2-{count}-{rate}'; t0 = time.monotonic()
        cmd = f'python3 /tmp/tcp-flows.py client --count {count} --rate {rate} --seconds 120 --hold 30'
        say('$ guest-command client', cmd)
        proc = subprocess.Popen(['/work/field/guest-command', 'client', cmd], stdout=(raw / (name + '.jsonl')).open('w'), stderr=(raw / (name + '.err')).open('w'))
        lat = []; fails = 0
        with (raw / (name + '-api.jsonl')).open('w') as f:
            while proc.poll() is None and time.monotonic() - t0 < 200:
                t = time.monotonic(); p = kubectl(['get', 'interfaces', '-o', 'name'], quiet=True, timeout=10)
                l = time.monotonic() - t; lat.append(l); fails += p.returncode != 0
                f.write(json.dumps({'at': time.time(), 'api_latency_s': l, 'exit': p.returncode}) + '\n'); time.sleep(.5)
        if proc.poll() is None:
            proc.terminate()
        res = [json.loads(l) for l in (raw / (name + '.jsonl')).read_text().splitlines() if l.startswith('{')]
        hold = [r for r in res if r.get('phase') == 'hold']; lat.sort()
        summary[name] = {'completed': hold[0]['completed'] if hold else None, 'ratio': hold[0]['completion_ratio'] if hold else None,
                         'api_p99_s': lat[int(len(lat) * .99) - 1] if lat else None, 'api_failures': fails, 'api_samples': len(lat)}
        say(name, json.dumps(summary[name])); time.sleep(15)
    save('R5-r2.summary.json', summary)


def bgp_setup(count, ipv6):
    kubectl(['patch', 'bgprouter', 'default', '--type=merge', '-p', json.dumps({'spec': {'asn': 65000}})])
    v6 = ipv6 > 0
    extra = [obj('BGPPeer', 'g-u1v6', {'address': 'fd42:1::2', 'asn': 65001}), obj('BGPPeer', 'g-u2v6', {'address': 'fd42:2::2', 'asn': 65002})] if v6 else []
    apply(obj('Interface', 'g-wan', {'link': 'eth4', 'addresses': ['198.18.0.1/24'] + (['fd42:1::1/64'] if v6 else [])}),
          obj('Interface', 'g-wan20', {'link': 'eth4.20', 'vlan': {'parent': 'eth4', 'id': 20}, 'addresses': ['198.18.20.1/24'] + (['fd42:2::1/64'] if v6 else [])}),
          obj('BGPPeer', 'g-u1', {'address': '198.18.0.2', 'asn': 65001}), obj('BGPPeer', 'g-u2', {'address': '198.18.20.2', 'asn': 65002}), *extra,
          obj('FirewallZone', 'g-wan', {'links': ['eth4', 'eth4.20'], 'input': 'Accept', 'forwardTo': ['g-lan']}))
    guest('gateway', 'pkill bird; sleep 1; ip netns add upstream2 2>/dev/null; ip link show upstream2 >/dev/null 2>&1 || ip netns exec upstream2 ip link show upstream2 >/dev/null 2>&1 || ip link add link test0 name upstream2 type vlan id 20; '
          'ip link set upstream2 netns upstream2 2>/dev/null; ip netns exec upstream2 ip link set lo up; ip netns exec upstream2 ip addr replace 198.18.20.2/24 dev upstream2; '
          'ip netns exec upstream2 ip link set upstream2 up; ip netns exec upstream2 ip route replace default via 198.18.20.1; mkdir -p /run/field-bird; '
          'ip -6 addr replace fd42:1::2/64 dev test0; ip netns exec upstream2 ip -6 addr replace fd42:2::2/64 dev upstream2; true')
    for side, local in [(1, '198.18.0.2'), (2, '198.18.20.2')]:
        path = f'/work/field/upstream-r2-{side}.conf'
        subprocess.run(['python3', '/work/field/bgp-prefix-generator.py', path, '--ipv4', str(count), '--ipv6', str(ipv6), '--asn', str(65000 + side),
                        '--local', local, '--peer', '198.18.0.1' if side == 1 else '198.18.20.1'], check=True)
        put('gateway', f'/tmp/upstream-r2-{side}.conf', Path(path).read_text())


def bird_start(side):
    prefix = '' if side == 1 else 'ip netns exec upstream2 '
    guest('gateway', f'{prefix}bird -c /tmp/upstream-r2-{side}.conf -s /run/field-bird/r2-{side}.ctl -P /run/field-bird/r2-{side}.pid')


def peers():
    p = kubectl(['get', 'bgppeers', '-o', 'json'], quiet=True)
    try:
        return {i['metadata']['name']: i.get('status', {}) for i in json.loads(p.stdout)['items']}
    except Exception:
        return {}


def samples(count, tag):
    addrs = [str(ipaddress.IPv4Address(int(ipaddress.IPv4Address('10.64.0.0')) + i * max(1, count // 1000))) for i in range(1000)]
    for side in [1, 2]:
        prefix = '' if side == 1 else 'ip netns exec upstream2 '
        put('gateway', f'/tmp/R7-r2-samples-{side}.sh', '\n'.join(prefix + 'ip addr replace ' + a + '/32 dev lo' for a in addrs) + '\n')
        guest('gateway', f'sh /tmp/R7-r2-samples-{side}.sh', quiet=True)
    guest('client', 'ip route replace 10.64.0.0/10 via 192.168.10.1', quiet=True)
    put('client', '/tmp/R7-samples.txt', '\n'.join(addrs) + '\n')
    p = guest('client', 'fping -c 1 -t 1000 -i 2 -f /tmp/R7-samples.txt > /tmp/R7-fping.txt 2>&1; printf "answered %s of %s\\n" $(grep -c "1/1/0%" /tmp/R7-fping.txt) $(wc -l < /tmp/R7-samples.txt)', 120)
    (raw / f'R7-r2-{tag}-samples.txt').write_text(p.stdout + p.stderr)
    return p.stdout


def row_r7():
    counts = [int(x) for x in args[0].split(',')]; ipv6 = int(args[1]) if len(args) > 1 else 0
    for count in counts:
        tag = f'{count}' + (f'-v6-{ipv6}' if ipv6 else '') + '-' + os.environ.get('R2_RAM_MIB', '2048') + 'MiB'
        bgp_setup(count, ipv6); time.sleep(10)
        t0 = time.monotonic(); bird_start(1); bird_start(2); rows = []
        while time.monotonic() - t0 < 900:
            ps = peers(); qmem = subprocess.run(['python3', '/work/field/qmp.py', 'r2', 'query-memory-size-summary'], capture_output=True, text=True).stdout
            rss = Path('/proc/' + Path('/work/vms/r2/pid').read_text().strip() + '/status').read_text().split('VmRSS:')[1].split('\n')[0].strip()
            rows.append({'elapsed_s': time.monotonic() - t0, 'peers': ps, 'qemu_rss': rss})
            names = ('g-u1', 'g-u2') + (('g-u1v6', 'g-u2v6') if ipv6 else ())
            got = [ps.get(n, {}).get('routesReceived') for n in names]
            say(f'{time.monotonic() - t0:.0f}s received {got} states {[ps.get(n, {}).get("state") for n in ("g-u1", "g-u2")]} qemu rss {rss}')
            if got[:2] == [count, count] and all(g == ipv6 for g in got[2:]):
                break
            time.sleep(5)
        done = got[:2] == [count, count] and all(g == ipv6 for g in got[2:])
        out = {'count': count, 'ipv6': ipv6, 'converged_s': rows[-1]['elapsed_s'] if done else None, 'last_received': got, 'rows': rows}
        out['samples_tail'] = samples(count, tag)
        node = subprocess.run(['kubectl', '--server', 'https://127.0.0.1:5020', '--request-timeout=10s', 'get', 'osconfigs', '-o', 'yaml'],
                              env=dict(os.environ, KUBECONFIG='/work/trust/admin.kubeconfig'), capture_output=True, text=True)
        out['node_osconfig'] = node.stdout[-3000:]
        t = time.monotonic(); kubectl(['get', 'bgppeers'], quiet=True); out['api_latency_after_s'] = time.monotonic() - t
        save(f'R7-r2-{tag}.json', out)
        say(f'{tag}: converged {out["converged_s"]}s; samples: {out["samples_tail"].strip()[-200:]}')
        guest('gateway', 'pkill bird; true', quiet=True)


def row_r9():
    bgp_setup(1000, 0); time.sleep(10); bird_start(1); bird_start(2); time.sleep(40)
    say('peers', json.dumps({k: (v.get('state'), v.get('routesReceived')) for k, v in peers().items()}))
    samples(1000, 'r9-baseline')
    # upstream 2 announces the same prefixes with a longer AS path? both identical -> ECMP or first; observe loss gaps
    guest('client', 'rm -f /tmp/R9-probes.txt; nohup timeout 900 fping -D -l -p 100 10.64.0.0 >/tmp/R9-probes.txt 2>&1 </dev/null &')
    events = []
    def ev(phase, vm, cmd):
        t = time.time(); p = guest(vm, cmd); events.append({'at': t, 'phase': phase, 'command': cmd, 'exit': p.returncode, 'out': p.stdout + p.stderr})
        save('R9-r2-events.json', events)
    time.sleep(20)
    ev('hard-session-kill-u1', 'gateway', 'kill -9 $(cat /run/field-bird/r2-1.pid)'); time.sleep(40)
    ev('u1-back', 'gateway', 'bird -c /tmp/upstream-r2-1.conf -s /run/field-bird/r2-1.ctl -P /run/field-bird/r2-1.pid'); time.sleep(40)
    ev('silent-blackhole-u1-start', 'gateway', "nft add table inet fieldbh; nft 'add chain inet fieldbh in { type filter hook prerouting priority -300; policy accept; }'; "
       "nft add rule inet fieldbh in iifname test0 ip saddr 198.18.0.1 drop; nft 'add chain inet fieldbh out { type filter hook output priority -300; policy accept; }'; nft add rule inet fieldbh out oifname test0 ip daddr 198.18.0.1 drop")
    # wait for BIRD's hold timer on the DUT (default 240 s) and record when the session drops
    t0 = time.monotonic(); down = None
    while time.monotonic() - t0 < 320:
        s = peers().get('g-u1', {}).get('state')
        if s and s != 'Established':
            down = time.monotonic() - t0; break
        time.sleep(2)
    events.append({'phase': 'silent-blackhole-u1-detected', 'detect_s': down}); save('R9-r2-events.json', events)
    say('silent blackhole detected by the DUT after', down)
    ev('silent-blackhole-u1-end', 'gateway', 'nft delete table inet fieldbh'); time.sleep(60)
    for i in range(60):
        t = time.monotonic()
        ev(f'flap-{i}', 'gateway', f'birdc -s /run/field-bird/r2-1.ctl {"disable" if i % 2 == 0 else "enable"} dut4')
        time.sleep(max(0, 5 - (time.monotonic() - t)))
    time.sleep(30)
    settle = time.time(); ok = None
    while time.time() - settle < 60:
        ps = peers()
        if all(ps.get(n, {}).get('state') == 'Established' and ps.get(n, {}).get('routesReceived') == 1000 for n in ('g-u1', 'g-u2')):
            ok = time.time() - settle; break
        time.sleep(2)
    events.append({'phase': 'settled-after-flaps', 'settle_s_after_wait30': ok}); save('R9-r2-events.json', events)
    time.sleep(5)
    guest('client', 'pkill fping; sleep 1', quiet=True)
    data = subprocess.run(['/work/field/guest-command', 'client', 'cat /tmp/R9-probes.txt'], capture_output=True, text=True).stdout
    (raw / 'R9-r2-probes.txt').write_text(data)
    say('probe lines', len(data.splitlines()))
    guest('gateway', 'pkill bird; true', quiet=True)


{'attach': row_attach, 'detach': row_detach, 'profile': row_profile, 'r4': row_r4, 'r5': row_r5, 'r7': row_r7, 'r9': row_r9}[row]()
