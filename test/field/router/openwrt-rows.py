#!/usr/bin/env python3
"""R2, R3, R8 and R9 for OpenWrt 24.10.8 on G (reinstalled from the pinned image).

usage: openwrt-rows.py r2|r3|r8|r9
r2  sysupgrade: download cut at 25 %, truncated image (sysupgrade -T), power cut
    while the image is written, a real upgrade under traffic (24.10.8 -> 24.10.7,
    config kept) and back to 24.10.8. OpenWrt flashes the running disk in place.
r3  fw4 rule deleted from the kernel behind fw4's back; two uci writers on one
    option; a lockout re-applied every 10 s through rpcd apply with rollback.
r8  bird2 (installed from the release feed): prefix filters, export to transit,
    import limit 1000 (max-prefix), export edit, observer O on the LAN.
r9  bird2 with U2 on a fourth NIC (transit): carrier, blackhole, BFD, flapping,
    bird restart with and without the kernel protocol's persist option.
The second upstream for r9 needs OpenWrt started with FIELD_EXTRA_NET (eth3).
"""
import json, os, signal, subprocess, sys, time
from pathlib import Path

raw = Path('/work/field/raw'); row = sys.argv[1]; out = {}; events = []
PW = Path('/work/field/private/openwrt-root-password').read_text().strip()


def say(*a):
    print(time.strftime('%H:%M:%S', time.gmtime()), *a, flush=True)


def dut(cmd, quiet=False, timeout=60, data=None):
    if not quiet:
        say('$ openwrt-command', cmd[:150])
    try:
        p = subprocess.run(['/work/field/openwrt-command', cmd], input=data, capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        p = subprocess.CompletedProcess([], 124, '', 'client timeout')
    if not quiet and (p.stdout + p.stderr).strip():
        print((p.stdout + p.stderr).strip()[-600:], flush=True)
    return p


def guest(vm, cmd, quiet=False, data=None, timeout=60):
    if not quiet:
        say('$ guest-command', vm, cmd[:150])
    p = subprocess.run(['/work/field/guest-command', vm, cmd], input=data, capture_output=True, text=True, timeout=timeout)
    if not quiet and (p.stdout + p.stderr).strip():
        print((p.stdout + p.stderr).strip()[-400:], flush=True)
    return p


def qmp(cmd, args=None, vm='openwrt'):
    subprocess.run(['python3', '/work/field/qmp.py', vm, cmd] + ([json.dumps(args)] if args is not None else []), check=True, capture_output=True)


def save():
    (raw / f'{row.upper()}-openwrt-rows.summary.json').write_text(json.dumps(out, indent=2) + '\n')


def ev(phase, **kw):
    events.append({'at': time.time(), 'phase': phase, **kw})
    (raw / f'{row.upper()}-openwrt-rows-events.json').write_text(json.dumps(events, indent=2) + '\n')


def wait_ssh(limit=300):
    t = time.monotonic()
    while time.monotonic() - t < limit:
        if dut('true', quiet=True, timeout=8).returncode == 0:
            return round(time.monotonic() - t, 1)
        time.sleep(2)


def wait_fwd(t0, limit=300):
    while time.monotonic() - t0 < limit:
        if subprocess.run(['/work/field/guest-command', 'client', 'ping -c1 -W1 198.19.0.1'], capture_output=True).returncode == 0:
            return round(time.monotonic() - t0, 1)
        time.sleep(1)


def release():
    return dut('. /etc/openwrt_release; echo $DISTRIB_RELEASE $DISTRIB_REVISION', quiet=True).stdout.strip()


def probes_start(tag, seconds=3000, target='198.19.0.1'):
    guest('client', f'pkill fping; rm -f /tmp/{tag}.txt; nohup timeout {seconds} fping -D -l -p 100 {target} >/tmp/{tag}.txt 2>&1 </dev/null &', quiet=True)
    return float(guest('client', 'date +%s.%N', quiet=True).stdout) - time.time()


def probes_gaps(tag, skew, windows):
    guest('client', 'pkill fping; sleep 1', quiet=True)
    data = guest('client', f'cat /tmp/{tag}.txt', quiet=True).stdout
    (raw / f'{tag}.txt').write_text(data)
    rep = [float(l[1:l.index(']')]) - skew for l in data.splitlines() if l.startswith('[') and 'bytes' in l]
    gaps = [(a, b - a) for a, b in zip(rep, rep[1:])]
    res = {'replies': len(rep)}
    for name, (a, b) in windows.items():
        g = sorted(d for t, d in gaps if a - 1 <= t <= b)
        res[name] = {'max_gap_s': round(g[-1], 2) if g else None, 'gaps_over_0_3s': [round(x, 2) for x in g if x > .3]}
    return res


def pid():
    return int(Path('/work/vms/openwrt/pid').read_text())


def power_cut():
    argv = [x.decode() for x in Path(f'/proc/{pid()}/cmdline').read_bytes().split(b'\0') if x]
    p = pid(); os.kill(p, signal.SIGKILL); say('$ kill -9 qemu openwrt  # power cut')
    for _ in range(100):
        if not Path('/work/vms/openwrt/pid').exists():
            break
        time.sleep(.1)
    Path('/work/vms/openwrt/pid').unlink(missing_ok=True)
    time.sleep(1); subprocess.run(argv, check=True); return time.monotonic()


# ------------------------------------------------------------------ R2
if row == 'r2':
    URL = 'http://10.0.2.2:8091/{mode}/{name}'
    out['before'] = release(); say('running', out['before'])
    skew = probes_start('R2-openwrt-fwd')
    win = {}
    for mode in ('disconnect', 'truncated'):
        ev(mode); t = time.monotonic()
        p = dut(f'rm -f /tmp/fw.img.gz; wget -q -O /tmp/fw.img.gz {URL.format(mode=mode, name="openwrt-24.10.7.img.gz")}; echo wget=$?; ls -l /tmp/fw.img.gz; '
                'sysupgrade -T /tmp/fw.img.gz; echo test=$?; rm -f /tmp/fw.img.gz', timeout=120)
        out[mode] = {'seconds': round(time.monotonic() - t, 1), 'tail': (p.stdout + p.stderr).strip().splitlines()[-6:], 'release_after': release()}
        ev(mode + '-done'); win[mode] = (events[-2]['at'], events[-1]['at'])
    # power cut while sysupgrade writes the disk
    ev('cut-during-flash')
    dut(f'wget -q -O /tmp/fw.img.gz {URL.format(mode="full", name="openwrt-24.10.7.img.gz")}; sysupgrade -T /tmp/fw.img.gz; echo test=$?', timeout=120)
    subprocess.Popen(['/work/field/openwrt-command', 'sysupgrade -v /tmp/fw.img.gz'], stdout=open(raw / 'R2-openwrt-cut-sysupgrade.txt', 'w'), stderr=subprocess.STDOUT)
    t = time.monotonic(); con = Path('/work/vms/openwrt/console.log'); size0 = con.stat().st_size
    while time.monotonic() - t < 15:   # cut once the console shows the upgrade, else 15 s in (the write is under way by then)
        tail = con.read_bytes()[size0:].decode(errors='replace')
        if 'Writing image' in tail or 'Performing system upgrade' in tail or 'upgrade:' in tail:
            break
        time.sleep(.2)
    time.sleep(1.5)
    (raw / 'R2-openwrt-cut-console-before.txt').write_bytes(Path('/work/vms/openwrt/console.log').read_bytes()[size0:])
    t_cut = power_cut(); ssh = wait_ssh(300); fwd = wait_fwd(t_cut, 300)
    out['cut_during_flash'] = {'cut_after_s': round(t_cut - t, 1), 'ssh_back_s': ssh, 'forwarding_back_s': fwd, 'release_after': release() if ssh else None}
    (raw / 'R2-openwrt-cut-console-after.txt').write_bytes(Path('/work/vms/openwrt/console.log').read_bytes()[size0:])
    say('cut during flash:', json.dumps(out['cut_during_flash'])); ev('cut-during-flash-done'); win['cut-during-flash'] = (events[-2]['at'], events[-1]['at']); save()
    if ssh is None:
        out['note'] = 'not reachable after the power cut during sysupgrade: unbootable outcome'
        save(); sys.exit(0)
    # real upgrade under traffic, config kept
    ev('upgrade'); dut(f'rm -f /tmp/fw.img.gz; wget -q -O /tmp/fw.img.gz {URL.format(mode="full", name="openwrt-24.10.7.img.gz")}', timeout=120)
    dut('sysupgrade -v /tmp/fw.img.gz >/tmp/sysupgrade.log 2>&1 &', timeout=20); t = time.monotonic(); time.sleep(10)
    ssh = wait_ssh(300); fwd = wait_fwd(t, 300)
    out['upgrade'] = {'ssh_back_s': round(time.monotonic() - t, 1) if ssh is not None else None, 'forwarding_back_s': fwd, 'release_after': release(),
                      'config_kept': '198.18.0.1' in dut('uci get network.wan.ipaddr', quiet=True).stdout}
    say('upgrade:', json.dumps(out['upgrade'])); ev('upgrade-done'); win['upgrade'] = (events[-2]['at'], events[-1]['at'])
    # back to 24.10.8
    ev('back'); dut(f'rm -f /tmp/fw.img.gz; wget -q -O /tmp/fw.img.gz {URL.format(mode="full", name="openwrt-24.10.8-sysupgrade.img.gz")}', timeout=120)
    dut('sysupgrade -v /tmp/fw.img.gz >/tmp/sysupgrade.log 2>&1 &', timeout=20); t = time.monotonic(); time.sleep(10)
    ssh = wait_ssh(300); fwd = wait_fwd(t, 300)
    out['back'] = {'ssh_back_s': round(time.monotonic() - t, 1) if ssh is not None else None, 'forwarding_back_s': fwd, 'release_after': release()}
    say('back to 24.10.8:', json.dumps(out['back'])); ev('back-done'); win['back'] = (events[-2]['at'], events[-1]['at'])
    out['probes'] = probes_gaps('R2-openwrt-fwd', skew, win)
    out['crash_and_noforward_builds'] = 'unsupported: sysupgrade has no health-gated promotion or second slot to fall back to'
    save(); say('gaps:', json.dumps(out['probes']))

# ------------------------------------------------------------------ R3
if row == 'r3':
    dut("uci set firewall.fielddrift=rule; uci set firewall.fielddrift.name=fielddrift; uci set firewall.fielddrift.src=lan; uci set firewall.fielddrift.dest=wan; "
        "uci set firewall.fielddrift.proto=udp; uci set firewall.fielddrift.dest_port=9077; uci set firewall.fielddrift.target=DROP; uci commit firewall; fw4 reload")
    before = dut("nft -a list ruleset | grep -n fielddrift")
    dut("nft -a list ruleset | awk '/chain forward_lan/{c=1} c && /fielddrift/{print $NF; exit}' | xargs -I H nft delete rule inet fw4 forward_lan handle H; nft list ruleset | grep -c fielddrift")
    t0 = time.monotonic(); back = None; polls = []
    while time.monotonic() - t0 < 40:
        n = dut('nft list ruleset | grep -c fielddrift', quiet=True).stdout.strip()
        polls.append({'t': round(time.monotonic() - t0, 1), 'rules': n})
        if n not in ('', '0'):
            back = polls[-1]['t']; break
        time.sleep(2)
    out['drift'] = {'before': before.stdout, 'repaired_s': back, 'polls': polls}; say('drift: repaired after', back)
    dut('uci delete firewall.fielddrift; uci commit firewall; fw4 reload', quiet=True)
    races = []
    import threading
    for i in range(5):
        res = {}
        def w(name):
            p = dut(f'uci set firewall.@defaults[0].field_writer={name}-{i}; uci commit firewall; echo ok', quiet=True)
            res[name] = {'exit': p.returncode, 'out': p.stdout.strip()[-80:]}
        ts = [threading.Thread(target=w, args=(n,)) for n in ('a', 'b')]; [t.start() for t in ts]; [t.join() for t in ts]
        res['final'] = dut('uci get firewall.@defaults[0].field_writer', quiet=True).stdout.strip(); races.append(res)
    say('writers:', json.dumps(races)); out['writers'] = races
    dut('uci delete firewall.@defaults[0].field_writer; uci commit firewall', quiet=True); save()
    # lockout replayed every 10 s through rpcd apply with rollback (production path)
    lines = ['S=$(ubus call session login \'{"username":"root","password":"%s"}\' | jsonfilter -e @.ubus_rpc_session)' % PW,
             "ubus call uci delete \"$(printf '%s' '{\"ubus_rpc_session\":\"SID\",\"config\":\"network\",\"section\":\"wan\",\"option\":\"gateway\"}' | sed \"s/SID/$S/\")\"",
             "ubus call uci apply \"$(printf '%s' '{\"ubus_rpc_session\":\"SID\",\"rollback\":true,\"timeout\":60}' | sed \"s/SID/$S/\")\"; echo apply_exit=$?"]
    script = '\n'.join(lines) + '\n'
    say('== replay: every 10 s delete network.wan.gateway + uci apply rollback=true timeout=60 over the production path, 6 min')
    log = []; t0 = time.monotonic()
    while time.monotonic() - t0 < 360:
        t = time.monotonic()
        try:
            p = subprocess.run(['ssh', '-F', '/work/field/private/production-ssh.conf', 'openwrt-production', 'sh', '-s'], input=script, capture_output=True, text=True, timeout=9)
            r = {'exit': p.returncode, 'tail': (p.stdout + p.stderr).strip().splitlines()[-2:]}
        except subprocess.TimeoutExpired:
            r = {'exit': 124, 'tail': ['client timeout']}
        st = dut('uci -q get network.wan.gateway; cat /proc/uptime', quiet=True, timeout=8)
        log.append({'t': round(t - t0, 1), **r, 'gateway': st.stdout.split()[0] if st.stdout.split() else None, 'uptime': st.stdout.split()[-2] if len(st.stdout.split()) > 1 else None})
        if len(log) % 6 == 0:
            say('replay', len(log), json.dumps(log[-1])[:300])
        time.sleep(max(0, 10 - (time.monotonic() - t)))
    w0 = time.monotonic(); settled = None
    while time.monotonic() - w0 < 240:
        if dut('uci -q get network.wan.gateway', quiet=True).stdout.strip() == '198.18.0.2' and guest('client', 'ping -c1 -W1 198.19.0.1', quiet=True).returncode == 0:
            settled = round(time.monotonic() - w0, 1); break
        time.sleep(5)
    out['replay'] = {'attempts': len(log), 'apply_accepted': sum(1 for r in log if 'apply_exit=0' in ' '.join(r['tail'])), 'settled_after_loop_s': settled, 'log': log}
    save(); say('replay:', json.dumps({k: v for k, v in out['replay'].items() if k != 'log'}))

# ------------------------------------------------------------------ R8 / R9 common: bird2 on OpenWrt
def bird_dut(conf):
    dut('cat >/etc/bird.conf', data=conf, quiet=True)
    p = dut('bird -p -c /etc/bird.conf && (birdc configure 2>/dev/null || (/etc/init.d/bird enable; /etc/init.d/bird restart)); sleep 2; birdc show protocols')
    return p


def obs_routes():
    p = guest('client', 'birdc -s /tmp/R8o.ctl show route', quiet=True)
    return sorted({l.split()[0] for l in p.stdout.splitlines() if l[:1].isdigit()})


if row == 'r8':
    dut('uci set firewall.fieldbgp=rule; uci set firewall.fieldbgp.name=fieldbgp; uci set firewall.fieldbgp.src=wan; uci set firewall.fieldbgp.proto=tcp; '
        'uci set firewall.fieldbgp.dest_port=179; uci set firewall.fieldbgp.target=ACCEPT; uci set network.wan20=interface; uci set network.wan20.device=eth0.20; '
        'uci set network.wan20.proto=static; uci set network.wan20.ipaddr=198.18.20.1; uci set network.wan20.netmask=255.255.255.0; '
        "uci add_list firewall.@zone[1].network=wan20; uci commit; /etc/init.d/network reload; fw4 reload; sleep 5; ip -br addr show eth0.20")
    guest('gateway', 'pkill bird; sleep 1; ip netns add upstream2 2>/dev/null; ip link show upstream2 >/dev/null 2>&1 || ip netns exec upstream2 ip link show upstream2 >/dev/null 2>&1 || ip link add link test0 name upstream2 type vlan id 20; '
          'ip link set upstream2 netns upstream2 2>/dev/null; ip netns exec upstream2 ip link set lo up; ip netns exec upstream2 ip addr replace 198.18.20.2/24 dev upstream2; ip netns exec upstream2 ip link set upstream2 up; mkdir -p /run/field-r8o; true')
    U1 = ('log stderr all; router id 198.18.0.2; protocol device {}\nprotocol static injected { ipv4; route 203.0.113.0/24 blackhole; route 203.0.113.128/25 blackhole; route 0.0.0.0/0 blackhole; route 198.51.100.0/24 blackhole; }\n'
          'protocol bgp dut4 { local 198.18.0.2 as 65001; neighbor 198.18.0.1 as 65000; hold time 9; keepalive time 3; ipv4 { import all; export where source = RTS_STATIC; }; }\n')
    U2 = ('log stderr all; router id 198.18.20.2; protocol device {}\nprotocol static injected { ipv4; route 198.51.100.0/24 blackhole; }\n'
          'protocol bgp dut4 { local 198.18.20.2 as 65002; neighbor 198.18.20.1 as 65000; hold time 9; keepalive time 3; ipv4 { import all; export where source = RTS_STATIC; }; }\n')
    guest('gateway', 'cat >/root/R8o-u1.conf', data=U1, quiet=True); guest('gateway', 'cat >/root/R8o-u2.conf', data=U2, quiet=True)
    guest('gateway', 'bird -c /root/R8o-u1.conf -s /run/field-r8o/u1.ctl -P /run/field-r8o/u1.pid; ip netns exec upstream2 bird -c /root/R8o-u2.conf -s /run/field-r8o/u2.ctl -P /run/field-r8o/u2.pid')
    OBS = ('log stderr all; router id 192.168.10.2; protocol device {}\nprotocol bgp dut { local 192.168.10.2 as 65003; neighbor 192.168.10.1 as 65000; hold time 9; keepalive time 3; ipv4 { import all; export none; }; }\n')
    guest('client', 'cat >/tmp/R8o.conf', data=OBS, quiet=True)
    guest('client', 'kill $(cat /tmp/R8o.pid 2>/dev/null) 2>/dev/null; sleep 1; bird -c /tmp/R8o.conf -s /tmp/R8o.ctl -P /tmp/R8o.pid')
    DUT = '''log syslog all; router id 198.18.0.1;
protocol device {}
protocol static local4 { ipv4; route 192.0.2.0/24 blackhole; }
define CUSTOMER = [ 203.0.113.0/24 ];
define LOCAL = [ 192.0.2.0/24 ];
define OBSERVER = [ 192.0.2.0/24, 203.0.113.0/24 ];
protocol bgp u1 { local 198.18.0.1 as 65000; neighbor 198.18.0.2 as 65001; hold time 9; keepalive time 3;
  ipv4 { import limit 1000 action disable; import where net ~ CUSTOMER; export where net ~ LOCAL; }; }
protocol bgp u2 { local 198.18.20.1 as 65000; neighbor 198.18.20.2 as 65002; hold time 9; keepalive time 3;
  ipv4 { import all; export where net ~ LOCAL; }; }
protocol bgp obs { local 192.168.10.1 as 65000; neighbor 192.168.10.2 as 65003; hold time 9; keepalive time 3;
  ipv4 { import none; export where net ~ OBSERVER; }; }
'''
    bird_dut(DUT); time.sleep(20)
    dut('birdc show protocols; birdc show route')
    out['observer_routes'] = obs_routes(); out['transit_export'] = dut('birdc show route export u2', quiet=True).stdout
    out['unauthorized_at_observer'] = [x for x in out['observer_routes'] if x not in ('192.0.2.0/24', '203.0.113.0/24')]
    say('observer sees', out['observer_routes'], '| unauthorized', out['unauthorized_at_observer'])
    guest('client', 'nohup ping -D -i .1 -w 200 198.19.0.1 >/tmp/R8o-fwd.txt 2>&1 </dev/null &', quiet=True)
    t0 = time.monotonic(); bird_dut(DUT.replace('define OBSERVER = [ 192.0.2.0/24, 203.0.113.0/24 ];', 'define OBSERVER = [ 192.0.2.0/24 ];')); gone = None
    while time.monotonic() - t0 < 60:
        if '203.0.113.0/24' not in obs_routes():
            gone = round(time.monotonic() - t0, 2); break
        time.sleep(.3)
    out['export_edit_withdrawn_s'] = gone; say('export edit withdrawn at O after', gone)
    bird_dut(DUT.replace('import where net ~ CUSTOMER;', 'import where net ~ [ 10.64.0.0/16+ ];'))
    subprocess.run(['python3', '/work/field/bgp-prefix-generator.py', '/work/field/R8o-max.conf', '--ipv4', '1001', '--asn', '65001', '--local', '198.18.0.2', '--peer', '198.18.0.1'], check=True)
    guest('gateway', 'cat >/root/R8o-u1.conf', data=Path('/work/field/R8o-max.conf').read_text(), quiet=True)
    t0 = time.monotonic(); guest('gateway', 'birdc -s /run/field-r8o/u1.ctl configure', quiet=True); hit = None; last = ''
    while time.monotonic() - t0 < 30:
        last = dut('birdc show protocols u1', quiet=True).stdout
        if 'down' in last or 'Disabled' in last or 'limit' in last.lower():
            hit = round(time.monotonic() - t0, 2); break
        time.sleep(.3)
    out['max_prefix'] = {'action_s': hit, 'state': last.strip().splitlines()[-1:] if last else None}
    say('max-prefix:', json.dumps(out['max_prefix']))
    save()
    guest('gateway', 'pkill bird; true', quiet=True); guest('client', 'kill $(cat /tmp/R8o.pid) 2>/dev/null; true', quiet=True)
    dut('/etc/init.d/bird stop; uci delete firewall.fieldbgp; uci delete network.wan20; uci commit; /etc/init.d/network reload; fw4 reload', quiet=True)

if row == 'r9':
    tif = os.environ.get('OPENWRT_TRANSIT_DEV') or dut("grep -l 52:54:00:30:00:04 /sys/class/net/*/address | cut -d/ -f5", quiet=True).stdout.strip() or 'eth3'
    dut(f'uci set network.transit=interface; uci set network.transit.device={tif}; uci set network.transit.proto=static; uci set network.transit.ipaddr=10.255.0.9; '
        'uci set network.transit.netmask=255.255.255.0; uci add_list firewall.@zone[1].network=transit; uci set firewall.fieldbgp=rule; uci set firewall.fieldbgp.name=fieldbgp; '
        'uci set firewall.fieldbgp.src=wan; uci set firewall.fieldbgp.proto="tcp udp"; uci set firewall.fieldbgp.dest_port="179 3784 3785"; uci set firewall.fieldbgp.target=ACCEPT; '
        f'uci commit; /etc/init.d/network reload; fw4 reload; sleep 5; ip -br addr show {tif}')
    GW = '''log stderr all;
router id 198.18.0.2;
protocol device {}
protocol static ann { ipv4; route 10.64.0.0/24 blackhole; }
protocol kernel { ipv4 { import none; export where source = RTS_BGP; }; }
protocol bfd { interface "*" { min rx interval 300 ms; min tx interval 300 ms; multiplier 3; }; }
protocol bgp u1 { local 198.18.0.2 as 65001; neighbor 198.18.0.1 as 65000; hold time 9; keepalive time 3; error wait time 1, 5; bfd BFDSW;
  ipv4 { import filter { if net = 192.168.10.0/24 then { bgp_local_pref = 200; accept; } reject; }; export where source = RTS_STATIC; }; }
protocol bgp u2 { local 10.255.0.4 as 65003; neighbor 10.255.0.9 as 65000; hold time 9; keepalive time 3; error wait time 1, 5;
  ipv4 { import where net = 192.168.10.0/24; export filter { if source = RTS_STATIC then { bgp_path.prepend(65003); bgp_path.prepend(65003); accept; } reject; }; }; }
'''
    DUT = '''log syslog all; router id 198.18.0.1;
protocol device {}
protocol direct { ipv4; interface "br-lan"; }
protocol kernel { ipv4 { import none; export where source = RTS_BGP; }; KPERSIST }
protocol bfd { interface "*" { min rx interval 300 ms; min tx interval 300 ms; multiplier 3; }; }
protocol bgp u1 { local 198.18.0.1 as 65000; neighbor 198.18.0.2 as 65001; hold time 9; keepalive time 3; BFDSW
  ipv4 { import all; export where net = 192.168.10.0/24; }; }
protocol bgp u2 { local 10.255.0.9 as 65000; neighbor 10.255.0.4 as 65003; hold time 9; keepalive time 3;
  ipv4 { import all; export where net = 192.168.10.0/24; }; }
'''
    def dutconf(bfd=False, persist=False):
        return DUT.replace('BFDSW', 'bfd on;' if bfd else '').replace('KPERSIST', 'persist;' if persist else '')
    guest('gateway', 'pkill bird; sleep 1; ip route del 192.168.10.0/24 via 198.18.0.1 2>/dev/null; mkdir -p /run/field-r9o; true')
    guest('gateway', 'cat >/root/R9o.conf', data=GW.replace(' bfd BFDSW;', ' bfd off;'), quiet=True)
    guest('gateway', 'bird -c /root/R9o.conf -s /run/field-r9o/bird.ctl -P /run/field-r9o/bird.pid')
    bird_dut(dutconf()); guest('client', 'ip route replace 10.64.0.0/10 via 192.168.10.1', quiet=True)
    def state():
        p = dut('birdc show protocols', quiet=True, timeout=15).stdout
        return {n: next((l.split()[3] + ':' + (l.split()[5] if len(l.split()) > 5 else '') for l in p.splitlines() if l.startswith(n + ' ')), None) for n in ('u1', 'u2')}
    def watch(seconds, tag, stop=None):
        t0 = time.monotonic(); last = None; tr = []
        while time.monotonic() - t0 < seconds:
            st = state()
            if st != last:
                tr.append({'t': round(time.monotonic() - t0, 2), 'state': st}); last = st; say(f'{tag} +{time.monotonic() - t0:5.1f}s', json.dumps(st))
                if stop and stop(st):
                    break
            time.sleep(.5)
        return tr
    up = lambda st: all(v and v.endswith('Established') for v in st.values())
    def downup():
        seen = {'d': False}
        def f(st):
            seen['d'] = seen['d'] or not up(st); return seen['d'] and up(st)
        return f
    watch(60, 'setup', stop=up); dut('ip route show 10.64.0.0/24'); guest('gateway', 'ip route show 192.168.10.0/24')
    skew = probes_start('R9-openwrt-probes', 1500, '10.64.0.0'); time.sleep(15); win = {}
    def phase(name, action, seconds, stop=None, after=None):
        ev(name); action(); tr = watch(seconds, name, stop)
        if after:
            after(); tr += watch(60, name + ' (restored)', stop=up)
        ev(name + '-done', transitions=tr); win[name] = (events[-2]['at'], events[-1]['at']); time.sleep(10)
    BH = ("nft add table inet r9obh; nft 'add chain inet r9obh pre { type filter hook prerouting priority -300; policy accept; }'; "
          "nft add rule inet r9obh pre iifname test0 ip saddr 198.18.0.1 drop; nft 'add chain inet r9obh out { type filter hook output priority -300; policy accept; }'; "
          "nft add rule inet r9obh out oifname test0 ip daddr 198.18.0.1 drop")
    phase('carrier', lambda: (qmp('set_link', {'name': 'wan', 'up': False}), qmp('set_link', {'name': 'site', 'up': False}, 'gateway')), 30,
          after=lambda: (qmp('set_link', {'name': 'wan', 'up': True}), qmp('set_link', {'name': 'site', 'up': True}, 'gateway')))
    phase('blackhole-hold', lambda: guest('gateway', BH, quiet=True), 40, stop=lambda st: not (st['u1'] or '').endswith('Established'),
          after=lambda: guest('gateway', 'nft delete table inet r9obh', quiet=True))
    bird_dut(dutconf(bfd=True)); guest('gateway', 'cat >/root/R9o.conf', data=GW.replace(' bfd BFDSW;', ' bfd on;'), quiet=True)
    guest('gateway', 'birdc -s /run/field-r9o/bird.ctl configure', quiet=True); watch(60, 'bfd up', stop=up); time.sleep(10)
    phase('blackhole-bfd', lambda: guest('gateway', BH, quiet=True), 40, stop=lambda st: not (st['u1'] or '').endswith('Established'),
          after=lambda: guest('gateway', 'nft delete table inet r9obh', quiet=True))
    def flap():
        for i in range(60):
            t = time.monotonic(); guest('gateway', f'birdc -s /run/field-r9o/bird.ctl {"disable" if i % 2 == 0 else "enable"} u1', quiet=True)
            time.sleep(max(0, 5 - (time.monotonic() - t)))
        guest('gateway', 'birdc -s /run/field-r9o/bird.ctl enable u1', quiet=True)
    phase('flapping', flap, 60, stop=up)
    phase('bird-restart', lambda: dut('/etc/init.d/bird restart', quiet=True), 90, stop=downup())
    bird_dut(dutconf(persist=True)); watch(60, 'persist up', stop=up); time.sleep(10)
    phase('bird-restart-persist', lambda: dut('/etc/init.d/bird restart', quiet=True), 90, stop=downup())
    out['probes'] = probes_gaps('R9-openwrt-probes', skew, win)
    out['transitions'] = {e['phase']: e.get('transitions') for e in events if e.get('transitions')}
    save(); say('gaps:', json.dumps({k: v for k, v in out['probes'].items()}))
    guest('gateway', 'pkill bird; ip route replace 192.168.10.0/24 via 198.18.0.1 dev test0; true', quiet=True)
    dut('/etc/init.d/bird stop; uci delete network.transit; uci delete firewall.fieldbgp; uci commit; /etc/init.d/network reload; fw4 reload', quiet=True)
