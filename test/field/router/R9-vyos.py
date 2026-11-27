#!/usr/bin/env python3
"""R9 for VyOS on topology G with a second upstream on a different link.

VyOS eth1 = G WAN (U1, gateway 198.18.0.2 AS65001), eth4 = transit segment
(U2, gateway 10.255.0.4 AS65003, AS path prepended so U1 is primary both ways).
All DUT configuration through the native CLI (configure/commit). The client
pings 10.64.0.0 (a gateway loopback announced by both) every 100 ms through VyOS.

Phases: hard session reset (kill -9 of the upstream BIRD), carrier down/up on
both ends of the U1 link (QMP set_link), silent blackhole of U1 with the link
up (hold 9 s), the same with BFD (300 ms x 3), U1 flapping every 5 s for 5 min,
FRR restart on the DUT (systemctl restart frr, no graceful restart).
"""
import json, os, subprocess, time
from pathlib import Path

raw = Path('/work/field/raw'); events = []
TIF = os.environ.get('VYOS_TRANSIT_IF', 'eth4')


def say(*a):
    print(time.strftime('%H:%M:%S', time.gmtime()), *a, flush=True)


def dut(cmd, quiet=False, timeout=60):
    if not quiet:
        say('$ vyos-command', cmd[:150])
    p = subprocess.run(['/work/field/vyos-command', cmd], capture_output=True, text=True, timeout=timeout)
    if not quiet and (p.stdout + p.stderr).strip():
        print((p.stdout + p.stderr).strip()[-500:], flush=True)
    return p


def vcfg(lines, quiet=False):
    script = 'source /opt/vyatta/etc/functions/script-template\nconfigure\n' + '\n'.join(lines) + '\ncommit\nexit\n'
    if not quiet:
        say('$ vyos-command vbash -s  # configure;', '; '.join(lines)[:220], '; commit')
    p = subprocess.run(['/work/field/vyos-command', 'vbash', '-s'], input=script, capture_output=True, text=True, timeout=300)
    if p.returncode or 'rror' in p.stdout + p.stderr:
        print((p.stdout + p.stderr)[-800:], flush=True)
    return p


def guest(vm, cmd, quiet=False, data=None, timeout=60):
    if not quiet:
        say('$ guest-command', vm, cmd[:150])
    p = subprocess.run(['/work/field/guest-command', vm, cmd], input=data, capture_output=True, text=True, timeout=timeout)
    if not quiet and (p.stdout + p.stderr).strip():
        print((p.stdout + p.stderr).strip()[-400:], flush=True)
    return p


def qmp(vm, cmd, args):
    say(f'$ qmp {vm} {cmd} {json.dumps(args)}')
    subprocess.run(['python3', '/work/field/qmp.py', vm, cmd, json.dumps(args)], check=True, capture_output=True)


def peers():
    p = dut("vtysh -c 'show bgp ipv4 unicast summary json'", quiet=True, timeout=20)
    try:
        j = json.loads(p.stdout)['peers']
        return {'u1': (j.get('198.18.0.2', {}).get('state'), j.get('198.18.0.2', {}).get('pfxRcd')),
                'u2': (j.get('10.255.0.4', {}).get('state'), j.get('10.255.0.4', {}).get('pfxRcd'))}
    except Exception:
        return {'error': (p.stdout + p.stderr)[-200:]}


def ev(phase, **kw):
    events.append({'at': time.time(), 'phase': phase, **kw})
    (raw / 'R9-vyos-events.json').write_text(json.dumps(events, indent=2) + '\n')


def watch(seconds, tag, stop=None):
    t0 = time.monotonic(); last = None; trans = []
    while time.monotonic() - t0 < seconds:
        cur = peers()
        if cur != last:
            trans.append({'t': round(time.monotonic() - t0, 2), 'peers': cur}); last = cur
            say(f'{tag} +{time.monotonic() - t0:5.1f}s', json.dumps(cur))
            if stop and stop(cur):
                break
        time.sleep(.5)
    return trans


BIRD = '''log stderr all;
router id 198.18.0.2;
protocol device {}
protocol static ann { ipv4; route 10.64.0.0/24 blackhole; }
protocol kernel { ipv4 { import none; export where source = RTS_BGP; }; }
protocol bfd { interface "*" { min rx interval 300 ms; min tx interval 300 ms; multiplier 3; }; }
protocol bgp u1 { local 198.18.0.2 as 65001; neighbor 198.18.0.1 as 65000; hold time 9; keepalive time 3; error wait time 1, 5; bfd BFD_U1;
  ipv4 { import filter { if net = 192.168.10.0/24 then { bgp_local_pref = 200; accept; } reject; }; export where source = RTS_STATIC; }; }
protocol bgp u2 { local 10.255.0.4 as 65003; neighbor 10.255.0.9 as 65000; hold time 9; keepalive time 3; error wait time 1, 5;
  ipv4 { import where net = 192.168.10.0/24;
         export filter { if source = RTS_STATIC then { bgp_path.prepend(65003); bgp_path.prepend(65003); accept; } reject; }; }; }
'''

# --- setup ---
vcfg([f'set interfaces ethernet {TIF} address 10.255.0.9/24',
      'set protocols bgp system-as 65000', 'set protocols bgp parameters router-id 198.18.0.1',
      'set protocols bgp address-family ipv4-unicast network 192.168.10.0/24',
      'set protocols bgp neighbor 198.18.0.2 remote-as 65001', 'set protocols bgp neighbor 198.18.0.2 timers holdtime 9',
      'set protocols bgp neighbor 198.18.0.2 timers keepalive 3', 'set protocols bgp neighbor 198.18.0.2 address-family ipv4-unicast',
      'set protocols bgp neighbor 10.255.0.4 remote-as 65003', 'set protocols bgp neighbor 10.255.0.4 timers holdtime 9',
      'set protocols bgp neighbor 10.255.0.4 timers keepalive 3', 'set protocols bgp neighbor 10.255.0.4 address-family ipv4-unicast',
      'set protocols bgp parameters bestpath as-path multipath-relax'])
dut(f'ip -br addr show {TIF}')
conf = BIRD.replace(' bfd BFD_U1;', ' bfd off;')
guest('gateway', 'pkill bird; sleep 1; ip route del 192.168.10.0/24 via 198.18.0.1 2>/dev/null; mkdir -p /run/field-r9v; true')
guest('gateway', 'cat >/root/R9-vyos.conf', data=conf, quiet=True)
guest('gateway', 'bird -c /root/R9-vyos.conf -s /run/field-r9v/bird.ctl -P /run/field-r9v/bird.pid')
guest('client', 'ip route replace 10.64.0.0/10 via 192.168.10.1', quiet=True)
watch(60, 'setup', stop=lambda c: c.get('u1', (0,))[0] == 'Established' and c.get('u2', (0,))[0] == 'Established')
dut("ip route show 10.64.0.0/24; vtysh -c 'show bgp ipv4 unicast 10.64.0.0/24'")
guest('gateway', 'ip route show 192.168.10.0/24')
skew = float(guest('client', 'date +%s.%N', quiet=True).stdout) - time.time()
ev('setup', peers=peers(), client_clock_minus_pod_s=skew, dut_route=dut('ip route show 10.64.0.0/24', quiet=True).stdout.strip())
guest('client', 'rm -f /tmp/R9v.txt; nohup timeout 1500 fping -D -l -p 100 10.64.0.0 >/tmp/R9v.txt 2>&1 </dev/null &')
time.sleep(15)

up = lambda c: c.get('u1', (0,))[0] == 'Established' and c.get('u2', (0,))[0] == 'Established'


def downup():
    seen = {'down': False}
    def stop(c):
        seen['down'] = seen['down'] or not up(c)
        return seen['down'] and up(c)
    return stop


# hard session reset: kill -9 of the BIRD that holds both upstream sessions
ev('hard-reset'); guest('gateway', 'kill -9 $(cat /run/field-r9v/bird.pid); sleep 1; bird -c /root/R9-vyos.conf -s /run/field-r9v/bird.ctl -P /run/field-r9v/bird.pid')
ev('hard-reset-observed', transitions=watch(60, 'hard reset', stop=downup()))
time.sleep(10)

# carrier down on both ends of the U1 link
ev('carrier-down'); qmp('vyos', 'set_link', {'name': 'wan', 'up': False}); qmp('gateway', 'set_link', {'name': 'site', 'up': False})
d = watch(30, 'carrier down')
ev('carrier-up', transitions=d); qmp('vyos', 'set_link', {'name': 'wan', 'up': True}); qmp('gateway', 'set_link', {'name': 'site', 'up': True})
ev('carrier-up-observed', transitions=watch(90, 'carrier up', stop=up))
time.sleep(10)

# silent blackhole of U1 (link up), hold timer 9 s
BH = ("nft add table inet r9vbh; nft 'add chain inet r9vbh pre { type filter hook prerouting priority -300; policy accept; }'; "
      "nft add rule inet r9vbh pre iifname test0 ip saddr 198.18.0.1 drop; nft 'add chain inet r9vbh out { type filter hook output priority -300; policy accept; }'; "
      "nft add rule inet r9vbh out oifname test0 ip daddr 198.18.0.1 drop")
ev('blackhole-hold'); guest('gateway', BH)
ev('blackhole-hold-detected', transitions=watch(40, 'blackhole (hold 9 s)', stop=lambda c: c.get('u1', (0,))[0] != 'Established'))
time.sleep(5); guest('gateway', 'nft delete table inet r9vbh')
ev('blackhole-hold-end', transitions=watch(60, 'blackhole removed', stop=up))
time.sleep(10)

# the same with BFD on U1 (VyOS native BFD, BIRD BFD on the upstream)
vcfg(['set protocols bfd peer 198.18.0.2 interval receive 300', 'set protocols bfd peer 198.18.0.2 interval transmit 300',
      'set protocols bfd peer 198.18.0.2 interval multiplier 3', 'set protocols bgp neighbor 198.18.0.2 bfd'])
guest('gateway', 'cat >/root/R9-vyos.conf', data=BIRD.replace(' bfd BFD_U1;', ' bfd on;'), quiet=True)
guest('gateway', 'birdc -s /run/field-r9v/bird.ctl configure')
watch(60, 'bfd up', stop=up); time.sleep(10)
dut("vtysh -c 'show bfd peers brief'")
ev('blackhole-bfd'); guest('gateway', BH)
ev('blackhole-bfd-detected', transitions=watch(40, 'blackhole (BFD)', stop=lambda c: c.get('u1', (0,))[0] != 'Established'))
time.sleep(5); guest('gateway', 'nft delete table inet r9vbh')
ev('blackhole-bfd-end', transitions=watch(60, 'blackhole removed', stop=up))
time.sleep(10)

# U1 flaps every 5 s for 5 min
ev('flap-start')
for i in range(60):
    t = time.monotonic()
    guest('gateway', f'birdc -s /run/field-r9v/bird.ctl {"disable" if i % 2 == 0 else "enable"} u1', quiet=True)
    time.sleep(max(0, 5 - (time.monotonic() - t)))
guest('gateway', 'birdc -s /run/field-r9v/bird.ctl enable u1', quiet=True)
ev('flap-stop', transitions=watch(60, 'after flapping', stop=up))
time.sleep(10)

# FRR restart on the DUT, no graceful restart configured
ev('frr-restart'); dut('sudo systemctl restart frr', timeout=120)
ev('frr-restart-observed', transitions=watch(120, 'frr restart', stop=downup()))
time.sleep(10)

# --- collect ---
guest('client', 'pkill fping; sleep 1', quiet=True)
data = guest('client', 'cat /tmp/R9v.txt', quiet=True).stdout
(raw / 'R9-vyos-probes.txt').write_text(data)
replies = [float(l[1:l.index(']')]) - skew for l in data.splitlines() if l.startswith('[') and 'bytes' in l]
gaps = [(a, b - a) for a, b in zip(replies, replies[1:])]
at = {e['phase']: e['at'] for e in events}
win = {'hard-reset': ('hard-reset', 'carrier-down'), 'carrier': ('carrier-down', 'blackhole-hold'),
       'blackhole-hold': ('blackhole-hold', 'blackhole-hold-end'), 'blackhole-bfd': ('blackhole-bfd', 'blackhole-bfd-end'),
       'flapping': ('flap-start', 'flap-stop'), 'frr-restart': ('frr-restart', 'frr-restart-observed')}
summary = {'replies': len(replies), 'span_s': replies[-1] - replies[0] if replies else None}
for name, (a, b) in win.items():
    a, b = at[a], at[b]
    g = sorted(x for t, x in gaps if a - 1 <= t <= b)
    summary[name] = {'max_gap_s': round(g[-1], 3) if g else None, 'gaps_over_0_3s': [round(x, 3) for x in g if x > .3]}
for e in events:
    if 'transitions' in e:
        summary.setdefault('transitions', {})[e['phase']] = e['transitions']
(raw / 'R9-vyos.summary.json').write_text(json.dumps(summary, indent=2) + '\n')
for name in win:
    say(name, json.dumps(summary[name]))
# --- restore ---
guest('gateway', 'pkill bird; ip route replace 192.168.10.0/24 via 198.18.0.1 dev test0; true', quiet=True)
vcfg(['delete protocols bgp', 'delete protocols bfd', f'delete interfaces ethernet {TIF} address'], quiet=True)
