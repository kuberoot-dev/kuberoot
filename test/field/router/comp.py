#!/usr/bin/env python3
"""Competitor rows on the G fabric for VyOS and OpenWrt, through native config.

usage: comp.py PRODUCT ROW [args]
  PRODUCT: vyos | openwrt
  ROW:     boot | cap | b2prof PROFILE | r6 [sizes] | r4 | r5 | r9 | r11

Topology G: client 192.168.10.2 -> DUT (LAN eth1 .1, WAN eth0 198.18.0.1)
-> gateway 198.18.0.2 -> sink 198.19.0.1. Observer SSH (eth2) is never used
as a payload path. Every phase writes raw/<ROW>-<product>-*.json; nothing is
inferred from an empty result.
"""
import base64, json, os, re, subprocess, sys, time
from pathlib import Path

raw = Path('/work/field/raw')
product, row = sys.argv[1], sys.argv[2]
args = sys.argv[3:]
assert product in ('vyos', 'openwrt')
CMD = '/work/field/vyos-command' if product == 'vyos' else '/work/field/openwrt-command'
# VyOS names the NICs by MAC: eth1 = WAN, eth2 = LAN, eth3 = observer.
WAN, LAN = ('eth1', 'eth2') if product == 'vyos' else ('eth0', 'eth1')


def say(*a):
    print(time.strftime('%H:%M:%S', time.gmtime()), *a, flush=True)


def sh(script, timeout=180, quiet=False, show=None):
    """Root shell on the DUT over the observer NIC; script fed on stdin."""
    if not quiet:
        say(f'$ {Path(CMD).name}', show or script.strip().replace('\n', '; ')[:200])
    entry = ['sudo', 'sh', '-s'] if product == 'vyos' else ['sh', '-s']
    try:
        p = subprocess.run([CMD] + entry, input=script, capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        return subprocess.CompletedProcess([], 124, '', 'timeout')
    if not quiet:
        out = (p.stdout + p.stderr).strip()
        if out:
            print(out[-1500:], flush=True)
    return p


def cfg(lines, show=None, save=True, timeout=1800):
    """Native configuration transaction; returns (process, seconds)."""
    t = time.monotonic()
    if product == 'vyos':
        script = 'source /opt/vyatta/etc/functions/script-template\nconfigure\n' + '\n'.join(lines) + '\ncommit\n' + ('save\n' if save else '') + 'exit\n'
        say('$ vyos-command vbash -s  # configure;', show or '; '.join(lines)[:220], '; commit' + ('; save' if save else ''))
        try:
            p = subprocess.run([CMD, 'vbash', '-s'], input=script, capture_output=True, text=True, timeout=timeout)
        except subprocess.TimeoutExpired:
            p = subprocess.CompletedProcess([], 124, '', 'timeout')
    else:
        script = '\n'.join(lines) + '\n'
        say('$ openwrt-command sh -s  #', show or '; '.join(lines)[:220])
        try:
            p = subprocess.run([CMD, 'sh', '-s'], input=script, capture_output=True, text=True, timeout=timeout)
        except subprocess.TimeoutExpired:
            p = subprocess.CompletedProcess([], 124, '', 'timeout')
    dt = time.monotonic() - t
    out = (p.stdout + p.stderr).strip()
    if out:
        print(out[-1200:], flush=True)
    say(f'transaction exit={p.returncode} in {dt:.2f}s')
    return p, dt


def guest(vm, cmd, timeout=120, quiet=False):
    if not quiet:
        say('$ guest-command', vm, cmd[:200])
    p = subprocess.run(['/work/field/guest-command', vm, cmd], capture_output=True, text=True, timeout=timeout)
    if not quiet:
        out = (p.stdout + p.stderr).strip()
        if out:
            print(out[-1500:], flush=True)
    return p


def put(vm, path, data):
    subprocess.run(['/work/field/guest-command', vm, 'cat >' + path], input=data, text=True, check=True)


def save(name, obj):
    (raw / name).write_text(json.dumps(obj, indent=2) + '\n')
    say('saved raw/' + name)


def ping_ok():
    return guest('client', 'ping -c1 -W1 198.19.0.1 >/dev/null', quiet=True).returncode == 0


def wait_ssh(limit=300):
    t = time.monotonic()
    while time.monotonic() - t < limit:
        try:
            if subprocess.run([CMD, 'true'], capture_output=True, timeout=8).returncode == 0:
                return time.monotonic() - t
        except subprocess.TimeoutExpired:
            pass
        time.sleep(2)
    return None


def vm_pid():
    return int(Path(f'/work/vms/{product}/pid').read_text())


def vm_argv():
    return [x.decode() for x in Path(f'/proc/{vm_pid()}/cmdline').read_bytes().split(b'\0') if x]


def hard_restart():
    argv = vm_argv(); pid = vm_pid()
    subprocess.run(['python3', '/work/field/qmp.py', product, 'quit'], capture_output=True)
    for _ in range(100):
        if not Path(f'/proc/{pid}').exists():
            break
        time.sleep(.1)
    Path(f'/work/vms/{product}/pid').unlink(missing_ok=True)
    t0 = time.monotonic()
    for _ in range(20):
        p = subprocess.run(argv, capture_output=True, text=True)
        if p.returncode == 0:
            break
        time.sleep(1)
    return t0


TCP_PROBES = '/work/field/tcp-port-probes.py'


def start_probes(tag, seconds=7200):
    code = base64.b64encode(Path(TCP_PROBES).read_bytes()).decode()
    guest('client', f'echo {code} | base64 -d >/root/tcp-port-probes.py', quiet=True)
    guest('client', f'nohup python3 /root/tcp-port-probes.py --target 198.19.0.1 --forbidden 2399 --markers 2200-2215 '
                    f'--seconds {seconds} --output /root/{tag}.jsonl >/root/{tag}.log 2>&1 </dev/null &')


def stop_probes(tag):
    guest('client', 'pkill -INT -f tcp-port-probes.py; sleep 2')
    data = subprocess.check_output(['/work/field/guest-command', 'client', f'cat /root/{tag}.jsonl'])
    (raw / f'{tag}.jsonl').write_bytes(data)
    pr = [json.loads(l) for l in data.decode().splitlines() if l.strip()]
    forb = [x for x in pr if x['kind'] == 'forbidden']
    return {'probes': len(pr), 'forbidden_probes': len(forb),
            'forbidden_answered': sum(1 for x in forb if x['result'] == 'answered'),
            'leak_times': [x['t'] for x in forb if x['result'] == 'answered'][:100]}


CHECK = ("python3 -c 'import socket\ns=socket.socket();s.settimeout(0.4)\n"
         "try:\n s.connect((\"198.19.0.1\",%d));print(1)\nexcept ConnectionRefusedError:print(1)\nexcept Exception:print(0)'")


def passes(port):
    return guest('client', CHECK % port, quiet=True).stdout.strip() == '1'


def wait_marker(port, want=True, limit=600):
    t = time.monotonic()
    while time.monotonic() - t < limit:
        if passes(port) == want:
            return time.monotonic() - t
        time.sleep(0.2)
    return None


# ---------------------------------------------------------------- boot / R-B1
def row_boot():
    rows = []
    for i in range(int(args[0]) if args else 3):
        if not ping_ok():
            say('forwarding not up before boot trial; waiting')
            time.sleep(30)
        sh('sync', quiet=True)
        say(f'boot trial {i + 1}: QMP quit, start QEMU, wait for client->DUT->sink ping')
        t0 = hard_restart(); first = None; ssh = None
        while time.monotonic() - t0 < 300:
            if ping_ok():
                first = time.monotonic() - t0; break
            time.sleep(0.5)
        ssh = wait_ssh(200)
        ssh = None if ssh is None else time.monotonic() - t0
        rows.append({'trial': i + 1, 'first_forwarded_ping_s': first, 'observer_ssh_s': ssh})
        say(json.dumps(rows[-1]))
        time.sleep(20)
    st = sh('df -k / ; df -k; cat /proc/meminfo | head -5; uname -a', quiet=True).stdout
    size = subprocess.run(['du', '-b', '--apparent-size'] + [str(p) for p in Path(f'/work/vms/{product}').glob('sys*.*') if 'pre' not in p.name and 'after' not in p.name],
                          capture_output=True, text=True).stdout
    alloc = subprocess.run(['qemu-img', 'info', '--output=json', f'/work/vms/{product}/' + ('sys.qcow2' if product == 'vyos' else 'sys.img')], capture_output=True, text=True).stdout
    save(f'R-B1-{product}-boot.json', {'trials': rows, 'guest_state': st, 'disk_files': size, 'qemu_img_info': alloc})
    print(st[-900:])


# ---------------------------------------------------------------- R-cap
def row_cap():
    res = {}
    if product == 'vyos':
        key = sh('wg genkey', quiet=True).stdout.strip() or 'cFZ1aW5mb3JtYXRpb25ub3RzZWNyZXRrZXkxMjM0NTY='
        tests = {
            'vrrp': ['set high-availability vrrp group FIELD interface ' + LAN, 'set high-availability vrrp group FIELD vrid 10',
                     'set high-availability vrrp group FIELD address 192.168.10.254/24'],
            'stateful-failover-conntrack-sync': ['set service conntrack-sync interface ' + LAN, 'set service conntrack-sync failover-mechanism vrrp sync-group FIELDSYNC',
                                                 'set high-availability vrrp sync-group FIELDSYNC member FIELD'],
            'wireguard': ['set interfaces wireguard wg9 address 10.99.0.1/24', 'set interfaces wireguard wg9 port 51820', f'set interfaces wireguard wg9 private-key {key}'],
            'ipsec': ['set vpn ipsec esp-group FIELD proposal 1 encryption aes256', 'set vpn ipsec esp-group FIELD proposal 1 hash sha256',
                      'set vpn ipsec ike-group FIELD proposal 1 encryption aes256', 'set vpn ipsec ike-group FIELD proposal 1 hash sha256',
                      'set vpn ipsec ike-group FIELD proposal 1 dh-group 14', 'set vpn ipsec interface eth1'],
            'pppoe': ['set interfaces pppoe pppoe9 source-interface ' + LAN, 'set interfaces pppoe pppoe9 authentication username field',
                      'set interfaces pppoe pppoe9 authentication password field'],
            'dhcpv6-pd': ['set interfaces ethernet eth1 dhcpv6-options pd 0 interface eth2 sla-id 0', 'set interfaces ethernet eth1 dhcpv6-options pd 0 length 56'],
            'ipv6-ra': ['set interfaces ethernet eth2 address fd00:10::1/64', 'set service router-advert interface eth2 prefix fd00:10::/64'],
        }
        undo = {'vrrp': ['delete high-availability'], 'stateful-failover-conntrack-sync': ['delete service conntrack-sync', 'delete high-availability vrrp sync-group'],
                'wireguard': ['delete interfaces wireguard wg9'], 'ipsec': ['delete vpn ipsec'], 'pppoe': ['delete interfaces pppoe pppoe9'],
                'dhcpv6-pd': ['delete interfaces ethernet eth1 dhcpv6-options'], 'ipv6-ra': ['delete service router-advert', 'delete interfaces ethernet eth2 address fd00:10::1/64']}
        verify = {'vrrp': 'ip -br addr show eth2; pgrep -a keepalived', 'stateful-failover-conntrack-sync': 'pgrep -a conntrackd',
                  'wireguard': 'ip -br link show wg9; wg show wg9 listen-port', 'ipsec': 'pgrep -a charon || pgrep -a charon-systemd; swanctl --list-conns 2>&1 | head -3',
                  'pppoe': 'ip -br link show pppoe9; pgrep -a pppd', 'dhcpv6-pd': 'pgrep -a dhcp6c; pgrep -a wide-dhcpv6-client', 'ipv6-ra': 'pgrep -a radvd'}
        order = ['vrrp', 'stateful-failover-conntrack-sync', 'wireguard', 'ipsec', 'pppoe', 'dhcpv6-pd', 'ipv6-ra']
        for name in order:
            lines = tests[name] if name != 'stateful-failover-conntrack-sync' else tests['vrrp'] + tests[name]
            p, dt = cfg(lines, save=False)
            time.sleep(3)
            v = sh(verify[name])
            committed = p.returncode == 0 and not re.search(r'(?i)(commit failed|failed|error)', p.stdout + p.stderr)
            res[name] = {'commit_ok': committed, 'commit_output': (p.stdout + p.stderr)[-800:], 'runtime_evidence': v.stdout + v.stderr}
            cfg(undo[name] + (undo['vrrp'] if name == 'stateful-failover-conntrack-sync' else []), save=False)
    else:
        q = sh('opkg list-installed; ls /lib/netifd/proto/; ls /etc/init.d/; which keepalived conntrackd wg ip swanctl ipsec pppd odhcpd odhcp6c')
        inst = q.stdout
        res['packages_and_protocols'] = inst
        def has(x):
            return re.search(x, inst) is not None
        res['vrrp'] = {'installed': has(r'(?m)^keepalived'), 'note': 'package keepalived not in the default image' if not has(r'(?m)^keepalived') else 'installed'}
        res['stateful-failover-conntrack-sync'] = {'installed': has(r'(?m)^conntrackd')}
        res['wireguard'] = {'installed': has(r'(?m)^(kmod-wireguard|wireguard-tools)'), 'proto_handler': has(r'wireguard\.sh')}
        res['ipsec'] = {'installed': has(r'(?m)^strongswan')}
        res['pppoe'] = {'installed': has(r'(?m)^ppp-mod-pppoe'), 'proto_handler': has(r'ppp\.sh')}
        res['dhcpv6-pd'] = {'installed': has(r'(?m)^odhcp6c'), 'proto_handler': has(r'dhcpv6\.sh')}
        res['ipv6-ra'] = {'installed': has(r'(?m)^odhcpd')}
        # functional gates for what ships in the image
        if res['pppoe']['proto_handler']:
            p, _ = cfg(["uci set network.field_pppoe=interface", "uci set network.field_pppoe.proto=pppoe", "uci set network.field_pppoe.device=eth2",
                        "uci set network.field_pppoe.username=field", "uci set network.field_pppoe.password=field", "uci commit network", "ifup field_pppoe; sleep 6; pgrep -a pppd; ifstatus field_pppoe | head -20"])
            res['pppoe']['runtime_evidence'] = p.stdout + p.stderr
            cfg(["ifdown field_pppoe", "uci delete network.field_pppoe", "uci commit network"])
        if res['ipv6-ra']['installed']:
            p, _ = cfg(["uci set network.lan.ip6assign=64", "uci set network.globals.ula_prefix=fd00:10::/48", "uci set dhcp.lan.ra=server", "uci set dhcp.lan.dhcpv6=server",
                        "uci commit", "/etc/init.d/network reload; /etc/init.d/odhcpd restart; sleep 5; ip -6 addr show eth2; pgrep -a odhcpd"])
            res['ipv6-ra']['runtime_evidence'] = p.stdout + p.stderr
            g = guest('client', 'timeout 8 rdisc6 test0 2>&1 || (sleep 6; ip -6 addr show test0)')
            res['ipv6-ra']['client_evidence'] = g.stdout + g.stderr
    save(f'R-cap-{product}.json', res)
    for k, v in res.items():
        if isinstance(v, dict):
            print(k, {x: y for x, y in v.items() if x in ('commit_ok', 'installed', 'proto_handler')})


# ---------------------------------------------------------------- R-B2 profile
def row_b2prof():
    prof = args[0]
    if product == 'vyos':
        base = ['delete nat source rule 10', 'delete firewall ipv4 forward filter']
        if prof == 'routing':
            lines = base
        elif prof == 'nat':
            lines = base + ['set nat source rule 10 outbound-interface name eth1', 'set nat source rule 10 source address 192.168.10.0/24',
                            'set nat source rule 10 translation address masquerade']
        else:
            lines = base + ['set nat source rule 10 outbound-interface name eth1', 'set nat source rule 10 source address 192.168.10.0/24',
                            'set nat source rule 10 translation address masquerade', 'set firewall ipv4 forward filter default-action drop',
                            'set firewall ipv4 forward filter rule 1 action accept', 'set firewall ipv4 forward filter rule 1 state established',
                            'set firewall ipv4 forward filter rule 1 state related', 'set firewall ipv4 forward filter rule 2 action accept',
                            'set firewall ipv4 forward filter rule 2 inbound-interface name eth2']
        p, dt = cfg(lines)
        st = sh('nft list ruleset | head -80; ip -br addr; ethtool -l eth1 2>&1 | tail -4')
    else:
        if prof == 'routing':
            lines = ["/etc/init.d/firewall stop", "nft list ruleset | head"]
        elif prof == 'nat':
            lines = ["uci set firewall.@zone[1].masq='1'", "uci set firewall.@defaults[0].forward='ACCEPT'", "uci set firewall.@zone[1].forward='ACCEPT'",
                     "uci set firewall.@zone[1].input='ACCEPT'", "uci commit firewall", "/etc/init.d/firewall restart"]
        else:
            lines = ["uci set firewall.@zone[1].masq='1'", "uci set firewall.@defaults[0].forward='REJECT'", "uci set firewall.@zone[1].forward='REJECT'",
                     "uci set firewall.@zone[1].input='REJECT'", "uci commit firewall", "/etc/init.d/firewall restart"]
        p, dt = cfg(lines)
        st = sh('nft list ruleset | head -80; ip -br addr; ethtool -l eth0 2>&1 | tail -4')
    (raw / f'R-B2-{product}-{prof}-state.txt').write_text(st.stdout + st.stderr)
    if prof == 'routing':
        guest('sink', 'ip route replace 192.168.10.0/24 via 198.19.0.254 2>/dev/null || true; ip route', quiet=True)
    print('profile', prof, 'applied; forwarding check:', ping_ok())


# ---------------------------------------------------------------- R6
def r6_rules(n, marker):
    if product == 'vyos':
        out = ['firewall {', ' ipv4 {', '  forward {', '   filter {', '    default-action drop',
               '    rule 1 {', '     action accept', '     state established', '     state related', '    }',
               '    rule 2 {', '     action accept', '     protocol icmp', '    }']
        for i in range(n - 1):
            out += [f'    rule {10 + i} {{', '     action drop', '     protocol tcp', f'     destination {{', f'      port {40000 + i % 20000}', '     }',
                    '     source {', f'      address 198.51.{(i >> 8) & 255}.{i & 255}', '     }', '    }']
        out += [f'    rule {10 + n} {{', '     action accept', '     protocol tcp', '     destination {', f'      port {marker}', '     }', '    }']
        out += ['   }', '  }', ' }', '}']
        return '\n'.join(out) + '\n'
    lines = ["while uci -q delete firewall.@rule[0]; do :; done", "uci set firewall.@defaults[0].forward='DROP'",
             "uci set firewall.@zone[0].forward='DROP'", "while uci -q delete firewall.@forwarding[0]; do :; done"]
    for i in range(n - 1):
        lines += ['uci add firewall rule >/dev/null', f"uci set firewall.@rule[-1].name='r6-{i}'", "uci set firewall.@rule[-1].src='lan'",
                  "uci set firewall.@rule[-1].dest='wan'", "uci set firewall.@rule[-1].proto='tcp'", f"uci set firewall.@rule[-1].src_ip='198.51.{(i >> 8) & 255}.{i & 255}'",
                  f"uci set firewall.@rule[-1].dest_port='{40000 + i % 20000}'", "uci set firewall.@rule[-1].target='DROP'"]
    lines += ['uci add firewall rule >/dev/null', "uci set firewall.@rule[-1].name='r6-allow'", "uci set firewall.@rule[-1].src='lan'",
              "uci set firewall.@rule[-1].dest='wan'", "uci set firewall.@rule[-1].proto='tcp'", f"uci set firewall.@rule[-1].dest_port='{marker}'",
              "uci set firewall.@rule[-1].target='ACCEPT'"]
    return '\n'.join(lines) + '\n'


def strip_block(text, name):
    out, depth, skip = [], 0, False
    for line in text.splitlines(keepends=True):
        if depth == 0 and line.startswith(name + ' {'):
            skip = True
        if skip:
            depth += line.count('{') - line.count('}')
            if depth == 0:
                skip = False
            continue
        out.append(line)
    return ''.join(out)


def r6_apply(n, marker):
    text = r6_rules(n, marker)
    if product == 'vyos':
        # Native atomic replace: load a full config whose firewall section is the new set
        # (merge refuses uncommitted changes, and delete+commit+merge would open a window).
        cur = sh('cat /config/config.boot', quiet=True).stdout
        put_dut('/tmp/r6.boot', strip_block(cur, 'firewall') + text)
        return cfg(['load /tmp/r6.boot'], show=f'load /tmp/r6.boot (running config with {n} forward rules)')
    put_dut('/tmp/r6.sh', text)
    return cfg(['sh /tmp/r6.sh', 'uci commit firewall', 'fw4 reload'], show=f'sh /tmp/r6.sh ({n} uci rules); uci commit firewall; fw4 reload')


def put_dut(path, data):
    entry = ['sudo', 'tee', path] if product == 'vyos' else ['sh', '-c', f'cat > {path}']
    subprocess.run([CMD] + entry, input=data, text=True, check=True, stdout=subprocess.DEVNULL)


def r6_edit(n, newport):
    if product == 'vyos':
        return cfg([f'set firewall ipv4 forward filter rule {10 + n} destination port {newport}', f'delete firewall ipv4 forward filter rule {10 + n} destination port {newport - 8}'])
    return cfg([f"uci set firewall.@rule[-1].dest_port='{newport}'", 'uci commit firewall', 'fw4 reload'])


def r6_desc(n):
    if product == 'vyos':
        return cfg(['set firewall ipv4 forward filter rule 10 description unrelated-change'])
    return cfg(["uci set firewall.@rule[0].name='r6-0-renamed'", 'uci commit firewall', 'fw4 reload'])


def r6_invalid(n):
    if product == 'vyos':
        return cfg(['set firewall ipv4 forward filter rule 9 action accept', 'set firewall ipv4 forward filter rule 9 protocol tcp',
                    'set firewall ipv4 forward filter rule 9 destination port 2399-99999'])
    return cfg(['uci add firewall rule >/dev/null', "uci set firewall.@rule[-1].name='r6-invalid'", "uci set firewall.@rule[-1].src='lan'",
                "uci set firewall.@rule[-1].dest='wan'", "uci set firewall.@rule[-1].proto='tcp'", "uci set firewall.@rule[-1].dest_port='2399-99999'",
                "uci set firewall.@rule[-1].target='ACCEPT'", 'uci commit firewall', 'fw4 reload; echo reload_exit=$?', 'uci delete firewall.@rule[-1]', 'uci commit firewall'])


def row_r6():
    sizes = [int(x) for x in args] or [100, 1000, 10000, 20000]
    guest('sink', 'ss -ltn | head -5')
    tag = f'R6-{product}-probes'
    p, _ = r6_apply(2, 2200)
    if wait_marker(2200, True, 60) is None or passes(2201):
        raise SystemExit('baseline deny/allow fixture not effective')
    start_probes(tag)
    rows = []
    marker = 2200
    for n in sizes:
        marker += 1
        e = {'count': n}
        say(f'== {n} rules, final allow on {marker}')
        w0 = time.time(); t0 = time.monotonic()
        p, dt = r6_apply(n, marker)
        e.update(apply_exit=p.returncode, apply_s=dt, apply_wall=w0, apply_tail=(p.stdout + p.stderr)[-400:])
        k = wait_marker(marker, True, 900)
        e['kernel_effect_from_apply_start_s'] = None if k is None else time.monotonic() - t0
        e['nft_lines'] = sh('nft list ruleset | wc -l', quiet=True).stdout.strip()
        say(f'apply {dt:.1f}s, marker forwarded after {e["kernel_effect_from_apply_start_s"]}s, nft lines {e["nft_lines"]}')
        edit = marker + 8
        w0 = time.time(); t0 = time.monotonic()
        p, dt = r6_edit(n, edit)
        k = wait_marker(edit, True, 300)
        e.update(edit_exit=p.returncode, edit_api_s=dt, edit_kernel_s=None if k is None else time.monotonic() - t0, edit_wall=w0)
        w0 = time.time(); p, dt = r6_desc(n); e.update(desc_exit=p.returncode, desc_s=dt, desc_wall=w0)
        w0 = time.time(); p, dt = r6_invalid(n)
        e.update(invalid_exit=p.returncode, invalid_s=dt, invalid_out=(p.stdout + p.stderr)[-600:], invalid_wall=w0,
                 marker_after_invalid=passes(edit), forbidden_after_invalid=passes(2399))
        # reboot with this set
        w0 = time.time(); t0 = time.monotonic()
        sh('sync; (sleep 1; reboot) >/dev/null 2>&1 &')
        time.sleep(5)
        k = wait_marker(edit, True, 600)
        e.update(reboot_wall=w0, reboot_marker_back_s=None if k is None else time.monotonic() - t0)
        wait_ssh(300)
        e['nft_lines_after_reboot'] = sh('nft list ruleset | wc -l', quiet=True).stdout.strip()
        say(f'reboot: marker back after {e["reboot_marker_back_s"]}s; nft lines {e["nft_lines_after_reboot"]}')
        rows.append(e); save(f'R6-{product}-scale.json', rows)
        if e['kernel_effect_from_apply_start_s'] is None or (dt > 1500):
            say('stopping size ladder: no effect / too slow'); break
    s = stop_probes(tag); s['rows'] = rows
    save(f'R6-{product}.summary.json', s)
    say(json.dumps({k: s[k] for k in ['probes', 'forbidden_probes', 'forbidden_answered']}))
    r6_restore()


def r6_restore():
    if product == 'vyos':
        cfg(['delete firewall ipv4 forward filter'])
    else:
        cfg(["while uci -q delete firewall.@rule[0]; do :; done", "uci set firewall.@defaults[0].forward='ACCEPT'", "uci set firewall.@zone[0].forward='ACCEPT'",
             "uci add firewall forwarding >/dev/null", "uci set firewall.@forwarding[-1].src='lan'", "uci set firewall.@forwarding[-1].dest='wan'", 'uci commit firewall', 'fw4 reload'])


# ---------------------------------------------------------------- R4
def dhcp_reset():
    if product == 'vyos':
        sh('systemctl stop kea-dhcp4-server 2>/dev/null; for f in /config/dhcp/dhcp4-leases.csv* /var/lib/kea/dhcp4.leases*; do [ -f "$f" ] && : > "$f"; done; systemctl start kea-dhcp4-server; sleep 2; systemctl is-active kea-dhcp4-server')
    else:
        sh('/etc/init.d/dnsmasq stop; : > /tmp/dhcp.leases; /etc/init.d/dnsmasq start; sleep 2; pgrep -a dnsmasq | head -2')


def dhcp_leases():
    if product == 'vyos':
        return sh('cat /config/dhcp/dhcp4-leases.csv 2>/dev/null || cat /var/lib/kea/dhcp4.leases', quiet=True).stdout
    return sh('cat /tmp/dhcp.leases', quiet=True).stdout


def row_r4():
    if product == 'vyos':
        n = 'service dhcp-server shared-network-name LAN subnet 192.168.10.0/24'
        cfg([f'set {n} subnet-id 1', f'set {n} option default-router 192.168.10.1', f'set {n} option name-server 192.168.10.1',
             f'set {n} range 0 start 192.168.10.100', f'set {n} range 0 stop 192.168.10.199', f'set {n} lease 3600'])
    else:
        cfg(["uci set dhcp.lan.start='100'", "uci set dhcp.lan.limit='100'", "uci set dhcp.lan.leasetime='1h'", 'uci commit dhcp', '/etc/init.d/dnsmasq restart'])
    for f in ['dhcp-pcap-summary.py']:
        code = base64.b64encode(Path('/work/field/' + f).read_bytes()).decode(); guest('client', f'echo {code} | base64 -d >/tmp/{f}', quiet=True)
    guest('client', 'nohup iperf3 -c 198.19.0.1 -u -b 1M -l 512 -t 400 -J >/tmp/R4-nat-load.json 2>&1 </dev/null &')
    phases = [('cold100', '--scenario avalanche -R 100 -p 30')] + [(f'rate-{r}', f'-r {r} -R 120 -n {r * 30} -p 30') for r in (10, 100, 1000)]
    out = {}
    for phase, options in phases:
        phase = 'mixed-' + phase
        dhcp_reset()
        guest('client', f'tcpdump -U -n -i test0 -s 0 -w /tmp/R4-{product}-{phase}.pcap "udp port 67 or udp port 68" >/tmp/R4-tcpdump.log 2>&1 & echo $! >/tmp/R4-capture.pid')
        time.sleep(1)
        command = f'timeout --signal=INT --kill-after=3s 35s perfdhcp -4 -B -l test0 -b mac=02:42:00:00:00:00 -s 42 {options} -x aelt > /tmp/R4-{product}-{phase}.log 2>&1; code=$?; tail -12 /tmp/R4-{product}-{phase}.log; exit $code'
        t0 = time.monotonic(); p = guest('client', command, timeout=60)
        guest('client', 'kill -INT $(cat /tmp/R4-capture.pid); sleep 1', quiet=True)
        leases = dhcp_leases(); (raw / f'R4-{product}-{phase}-leases.txt').write_text(leases)
        for ext in ['log', 'pcap']:
            q = subprocess.run(['/work/field/guest-command', 'client', f'cat /tmp/R4-{product}-{phase}.{ext}'], capture_output=True)
            (raw / f'R4-{product}-{phase}.{ext}').write_bytes(q.stdout)
        guest('client', f'python3 /tmp/dhcp-pcap-summary.py /tmp/R4-{product}-{phase}.pcap --output /tmp/R4-{product}-{phase}.summary.json', quiet=True)
        try:
            summary = json.loads(subprocess.check_output(['/work/field/guest-command', 'client', f'cat /tmp/R4-{product}-{phase}.summary.json']))
        except Exception as e:
            summary = {'error': str(e)}
        summary.update(product=product, phase=phase, perfdhcp_exit=p.returncode, wall_elapsed_s=time.monotonic() - t0,
                       lease_lines=len([l for l in leases.splitlines() if l.strip() and not l.startswith('address,')]))
        save(f'R4-{product}-{phase}.summary.json', summary)
        out[phase] = {k: v for k, v in summary.items() if k != 'leases'}
        print(json.dumps(out[phase])[:600], flush=True)
    # reboot with leases: count before and after
    before = dhcp_leases()
    sh('sync; (sleep 1; reboot) >/dev/null 2>&1 &'); time.sleep(10); wait_ssh(300); time.sleep(5)
    after = dhcp_leases()
    def cnt(t):
        return len([l for l in t.splitlines() if l.strip() and not l.startswith('address,')])
    save(f'R4-{product}-reboot.json', {'leases_before': cnt(before), 'leases_after': cnt(after), 'before': before[-3000:], 'after': after[-3000:]})
    say(f'leases before reboot {cnt(before)}, after {cnt(after)}')


# ---------------------------------------------------------------- R5
def row_r5():
    for vm in ['client', 'sink']:
        code = base64.b64encode(Path('/work/field/tcp-flows.py').read_bytes()).decode(); guest(vm, f'echo {code} | base64 -d >/tmp/tcp-flows.py', quiet=True)
    guest('sink', 'pkill -f "[t]cp-flows.py server"; nohup python3 /tmp/tcp-flows.py server --seconds 1500 >/tmp/R5-server.json 2>/tmp/R5-server.err </dev/null &')
    sh('cat /proc/sys/net/netfilter/nf_conntrack_max; grep -E "MemTotal|MemAvailable" /proc/meminfo')
    summary = {}
    for count, rate in [(1000, 1000), (10000, 1000), (100000, 10000)]:
        name = f'R5-{product}-{count}-{rate}'; t0 = time.monotonic()
        cmd = f'python3 /tmp/tcp-flows.py client --count {count} --rate {rate} --seconds 120 --hold 30'
        say('$ guest-command client', cmd)
        proc = subprocess.Popen(['/work/field/guest-command', 'client', cmd], stdout=(raw / (name + '.jsonl')).open('w'), stderr=(raw / (name + '.err')).open('w'))
        lat = []; peak = 0; mem = []
        with (raw / (name + '-resources.jsonl')).open('w') as f:
            while proc.poll() is None and time.monotonic() - t0 < 200:
                t = time.monotonic(); p = sh('cat /proc/sys/net/netfilter/nf_conntrack_count; grep MemAvailable /proc/meminfo', timeout=20, quiet=True)
                l = time.monotonic() - t; lat.append(l)
                f.write(json.dumps({'at': time.time(), 'mgmt_latency_s': l, 'exit': p.returncode, 'stdout': p.stdout}) + '\n')
                try:
                    v = p.stdout.split(); peak = max(peak, int(v[0])); mem.append(int(v[2]))
                except (IndexError, ValueError):
                    pass
                time.sleep(1)
        if proc.poll() is None:
            proc.terminate()
        res = [json.loads(l) for l in (raw / (name + '.jsonl')).read_text().splitlines() if l.startswith('{')]
        hold = [r for r in res if r.get('phase') == 'hold']
        lat.sort()
        summary[name] = {'completed': hold[0]['completed'] if hold else None, 'ratio': hold[0]['completion_ratio'] if hold else None,
                         'conntrack_peak': peak, 'mem_available_min_kb': min(mem) if mem else None,
                         'mgmt_p99_s': lat[int(len(lat) * .99) - 1] if lat else None, 'mgmt_failures': sum(1 for x in lat if x > 19)}
        say(name, json.dumps(summary[name]))
        time.sleep(15)
    # expiry: time until conntrack returns near baseline after close
    t0 = time.monotonic(); base = None
    while time.monotonic() - t0 < 200:
        p = sh('cat /proc/sys/net/netfilter/nf_conntrack_count', quiet=True)
        try:
            v = int(p.stdout.split()[0])
        except (IndexError, ValueError):
            v = None
        if v is not None and v < 2000:
            base = time.monotonic() - t0; break
        time.sleep(5)
    summary['conntrack_below_2000_after_last_close_s'] = base
    save(f'R5-{product}.summary.json', summary)


# ---------------------------------------------------------------- R11
def row_r11():
    def wr():
        q = json.loads(subprocess.check_output(['python3', '/work/field/qmp.py', product, 'query-blockstats']))
        return sum(v['stats']['wr_bytes'] for v in q if v.get('device') or v.get('qdev'))
    out = {}
    # config update rate: back-to-back native commits for 120 s
    w0 = wr(); t0 = time.monotonic(); n = 0; fails = 0; durs = []
    while time.monotonic() - t0 < 120:
        if product == 'vyos':
            p, dt = cfg([f'set protocols static route 198.51.100.0/24 next-hop 198.18.0.2 distance {1 + n % 200}'], show=f'set protocols static route 198.51.100.0/24 ... distance {1 + n % 200}')
        else:
            p, dt = cfg([f"uci set network.field_r11=route", "uci set network.field_r11.interface=wan", "uci set network.field_r11.target=198.51.100.0/24",
                         "uci set network.field_r11.gateway=198.18.0.2", f"uci set network.field_r11.metric={1 + n % 200}", 'uci commit network', '/etc/init.d/network reload'],
                        show=f'uci set network.field_r11.metric={1 + n % 200}; uci commit network; reload')
        n += 1; durs.append(dt); fails += p.returncode != 0
    el = time.monotonic() - t0; w = wr() - w0
    out['commit_loop'] = {'seconds': el, 'commits': n, 'failed': fails, 'rate_per_s': n / el, 'guest_write_bytes': w,
                          'bytes_per_commit': w / max(n, 1), 'extrapolated_bytes_per_day_at_achieved_rate': w * 86400 / el,
                          'extrapolated_bytes_per_day_at_10_per_s': (w / max(n, 1)) * 10 * 86400, 'commit_s_median': sorted(durs)[len(durs) // 2]}
    say(json.dumps(out['commit_loop']))
    # IOPS throttles during commits, forwarding observed in parallel
    for iops in (50, 10):
        throttle(iops)
        guest('client', 'rm -f /tmp/R11-fwd.txt; nohup timeout 75 fping -D -l -p 200 198.19.0.1 >/tmp/R11-fwd.txt 2>&1 </dev/null &', quiet=True)
        durs = []; fails = 0; t0 = time.monotonic()
        while time.monotonic() - t0 < 60:
            if product == 'vyos':
                p, dt = cfg([f'set protocols static route 198.51.100.0/24 next-hop 198.18.0.2 distance {len(durs) + 2}'], timeout=120)
            else:
                p, dt = cfg([f"uci set network.field_r11.metric={len(durs) + 2}", 'uci commit network', '/etc/init.d/network reload'], timeout=120)
            durs.append(dt); fails += p.returncode != 0
        time.sleep(16)
        fwd = guest('client', 'cat /tmp/R11-fwd.txt', quiet=True).stdout
        lost = fwd.count('timeout') + len(re.findall(r'unreachable', fwd))
        replies = len(re.findall(r'bytes, ', fwd))
        out[f'iops-{iops}'] = {'commits': len(durs), 'failed': fails, 'commit_s_max': max(durs), 'commit_s_median': sorted(durs)[len(durs) // 2],
                               'fwd_replies': replies, 'fwd_lost': lost}
        say(f'iops {iops}:', json.dumps(out[f'iops-{iops}']))
    throttle(0)
    # persistent FS full: fill, then a config change that must be saved
    fill = '/config/field-fill' if product == 'vyos' else '/overlay/field-fill'
    p = sh(f'df -k {Path(fill).parent}; dd if=/dev/zero of={fill} bs=1M 2>&1 | tail -1; sync; df -k {Path(fill).parent}')
    if product == 'vyos':
        pc, dt = cfg(['set protocols static route 198.51.102.0/24 next-hop 198.18.0.2'])
        after = sh('grep -c 198.51.102.0 /config/config.boot; ip route show 198.51.102.0/24', quiet=True)
    else:
        pc, dt = cfg(["uci set network.field_r11b=route", "uci set network.field_r11b.interface=wan", "uci set network.field_r11b.target=198.51.102.0/24",
                      "uci set network.field_r11b.gateway=198.18.0.2", 'uci commit network; echo commit_exit=$?', '/etc/init.d/network reload'])
        after = sh('grep -c 198.51.102.0 /etc/config/network; ip route show 198.51.102.0/24', quiet=True)
    out['fs-full'] = {'fill': p.stdout[-600:], 'commit_exit': pc.returncode, 'commit_out': (pc.stdout + pc.stderr)[-800:],
                      'persisted_and_kernel': after.stdout, 'forwarding_ok': ping_ok()}
    sh(f'rm -f {fill}; sync; df -k {Path(fill).parent}')  # remove only the fill file this test created
    say('fs-full', json.dumps({k: v for k, v in out['fs-full'].items() if k != 'fill'})[:500])
    save(f'R11-{product}.summary.json', out)


def throttle(iops):
    say(f'$ qmp {product} block_set_io_throttle iops={iops}')
    subprocess.run(['python3', '/work/field/qmp.py', product, 'block_set_io_throttle', json.dumps(
        {'id': qdev(), 'bps': 0, 'bps_rd': 0, 'bps_wr': 0, 'iops': iops, 'iops_rd': 0, 'iops_wr': 0})], check=True)


def qdev():
    q = json.loads(subprocess.check_output(['python3', '/work/field/qmp.py', product, 'query-block']))
    for b in q:
        if b.get('qdev') and 'pflash' not in b.get('device', '') and b.get('inserted') and not b.get('removable'):
            return b['qdev']
    raise SystemExit('no system disk qdev')


# ---------------------------------------------------------------- R7 (OpenWrt + bird2)
def row_r7():
    import ipaddress
    counts = [int(x) for x in args[0].split(',')] if args else [10000, 100000]
    ipv6 = int(args[1]) if len(args) > 1 else 0
    p = sh('command -v bird || (opkg update >/tmp/opkg-update.log 2>&1; opkg install bird2 bird2c 2>&1 | tail -3); bird --version 2>&1; ls -la $(command -v bird)')
    sh("""ip link add link eth0 name eth0.20 type vlan id 20 2>/dev/null; ip addr replace 198.18.20.1/24 dev eth0.20; ip link set eth0.20 up
ip -6 addr replace fd42:1::1/64 dev eth0; ip -6 addr replace fd42:2::1/64 dev eth0.20
nft insert rule inet fw4 input tcp dport 179 accept comment field-bgp 2>/dev/null || true
cat > /etc/bird.conf <<'CONF'
router id 198.18.0.1;
protocol device {}
protocol kernel k4 { ipv4 { export all; import none; }; merge paths on; }
protocol kernel k6 { ipv6 { export all; import none; }; }
protocol bgp u1 { local 198.18.0.1 as 65000; neighbor 198.18.0.2 as 65001; hold time 9; keepalive time 3; ipv4 { import all; export none; }; }
protocol bgp u2 { local 198.18.20.1 as 65000; neighbor 198.18.20.2 as 65002; hold time 9; keepalive time 3; ipv4 { import all; export none; }; }
protocol bgp u1v6 { local fd42:1::1 as 65000; neighbor fd42:1::2 as 65001; hold time 9; keepalive time 3; ipv6 { import all; export none; }; }
protocol bgp u2v6 { local fd42:2::1 as 65000; neighbor fd42:2::2 as 65002; hold time 9; keepalive time 3; ipv6 { import all; export none; }; }
CONF
/etc/init.d/bird restart 2>/dev/null || (killall bird; bird -c /etc/bird.conf); sleep 2; birdc show protocols""")
    guest('gateway', 'pkill bird; sleep 1; ip netns add upstream2 2>/dev/null; ip netns exec upstream2 ip link show upstream2 >/dev/null 2>&1 || { ip link add link test0 name upstream2 type vlan id 20; ip link set upstream2 netns upstream2; }; '
          'ip netns exec upstream2 ip link set lo up; ip netns exec upstream2 ip addr replace 198.18.20.2/24 dev upstream2; ip netns exec upstream2 ip link set upstream2 up; '
          'ip netns exec upstream2 ip route replace default via 198.18.20.1; ip -6 addr replace fd42:1::2/64 dev test0; ip netns exec upstream2 ip -6 addr replace fd42:2::2/64 dev upstream2; mkdir -p /run/field-bird')
    for count in counts:
        tag = f'{count}' + (f'-v6-{ipv6}' if ipv6 else '')
        for side, local in [(1, '198.18.0.2'), (2, '198.18.20.2')]:
            path = f'/work/field/upstream-{product}-{side}.conf'
            subprocess.run(['python3', '/work/field/bgp-prefix-generator.py', path, '--ipv4', str(count), '--ipv6', str(ipv6), '--asn', str(65000 + side),
                            '--local', local, '--peer', '198.18.0.1' if side == 1 else '198.18.20.1'], check=True)
            put('gateway', f'/tmp/upstream-{product}-{side}.conf', Path(path).read_text())
        guest('gateway', 'pkill bird; sleep 1; true', quiet=True)
        t0 = time.monotonic()
        for side in (1, 2):
            pre = '' if side == 1 else 'ip netns exec upstream2 '
            guest('gateway', f'{pre}bird -c /tmp/upstream-{product}-{side}.conf -s /run/field-bird/{product}-{side}.ctl -P /run/field-bird/{product}-{side}.pid')
        rows = []; fib = None
        while time.monotonic() - t0 < 900:
            q = sh("ip -4 route show proto bird | wc -l; ip -6 route show proto bird | wc -l; grep MemAvailable /proc/meminfo; birdc show protocols | grep -E '^u'", quiet=True, timeout=60)
            v = q.stdout.split()
            try:
                fib, fib6 = int(v[0]), int(v[1])
            except (IndexError, ValueError):
                fib, fib6 = None, None
            rows.append({'elapsed_s': time.monotonic() - t0, 'fib4': fib, 'fib6': fib6, 'text': q.stdout})
            say(f'{time.monotonic() - t0:.0f}s fib4={fib} fib6={fib6} {" ".join(v[2:4])}')
            if fib == count and (fib6 or 0) >= ipv6:
                break
            time.sleep(5)
        ok = fib == count and (rows[-1]['fib6'] or 0) >= ipv6
        addrs = [str(ipaddress.IPv4Address(int(ipaddress.IPv4Address('10.64.0.0')) + i * max(1, count // 1000))) for i in range(1000)]
        for side in (1, 2):
            pre = '' if side == 1 else 'ip netns exec upstream2 '
            put('gateway', f'/tmp/R7-samples-{side}.sh', '\n'.join(pre + 'ip addr replace ' + a + '/32 dev lo' for a in addrs) + '\n')
            guest('gateway', f'sh /tmp/R7-samples-{side}.sh', quiet=True)
        guest('client', 'ip route replace 10.64.0.0/10 via 192.168.10.1', quiet=True)
        put('client', '/tmp/R7-samples.txt', '\n'.join(addrs) + '\n')
        sp = guest('client', 'fping -c 1 -t 1000 -i 2 -f /tmp/R7-samples.txt 2>&1 | tail -3', 90)
        save(f'R7-{product}-{tag}.json', {'count': count, 'ipv6': ipv6, 'converged_s': rows[-1]['elapsed_s'] if ok else None, 'rows': rows, 'samples': sp.stdout + sp.stderr})
        guest('gateway', 'pkill bird; true', quiet=True)


{'r7': row_r7, 'boot': row_boot, 'cap': row_cap, 'b2prof': row_b2prof, 'r6': row_r6, 'r4': row_r4, 'r5': row_r5, 'r11': row_r11}[row]()
