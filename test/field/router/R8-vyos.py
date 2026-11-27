#!/usr/bin/env python3
"""R8 for VyOS: route policy with the native CLI (prefix-lists, route-maps,
maximum-prefix), same peers and prefixes as R8-debian.py.

U1 (customer, 198.18.0.2 AS65001) announces 203.0.113.0/24 (allowed),
203.0.113.128/25 (unauthorized more-specific), 0.0.0.0/0 and 198.51.100.0/24.
U2 (transit, 198.18.20.2 AS65002, VLAN 20) announces 198.51.100.0/24.
Observer O (the client, 192.168.10.2 AS65003) must see only 192.0.2.0/24
(local) and 203.0.113.0/24 (customer). Transit must get only 192.0.2.0/24.
Then: export edit under traffic (remove the customer prefix from O's export)
and max-prefix 1000 on U1 exceeded by one.
"""
import json, subprocess, time
from pathlib import Path

raw = Path('/work/field/raw'); out = {}


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
    s = 'source /opt/vyatta/etc/functions/script-template\nconfigure\n' + '\n'.join(lines) + '\ncommit\nexit\n'
    if not quiet:
        say('$ vyos-command vbash -s  # configure;', '; '.join(lines)[:200], '... commit')
    t = time.monotonic(); p = subprocess.run(['/work/field/vyos-command', 'vbash', '-s'], input=s, capture_output=True, text=True, timeout=300)
    if p.returncode or 'rror' in p.stdout + p.stderr:
        print((p.stdout + p.stderr)[-800:], flush=True)
    p.seconds = time.monotonic() - t
    return p


def guest(vm, cmd, quiet=False, data=None, timeout=60):
    if not quiet:
        say('$ guest-command', vm, cmd[:150])
    p = subprocess.run(['/work/field/guest-command', vm, cmd], input=data, capture_output=True, text=True, timeout=timeout)
    if not quiet and (p.stdout + p.stderr).strip():
        print((p.stdout + p.stderr).strip()[-400:], flush=True)
    return p


def observer_routes():
    p = guest('client', 'birdc -s /tmp/R8v-observer.ctl show route', quiet=True)
    return sorted({l.split()[0] for l in p.stdout.splitlines() if l[:1].isdigit()})


# --- upstreams and observer ---
guest('gateway', 'pkill bird; sleep 1; ip netns add upstream2 2>/dev/null; ip link show upstream2 >/dev/null 2>&1 || ip netns exec upstream2 ip link show upstream2 >/dev/null 2>&1 || ip link add link test0 name upstream2 type vlan id 20; '
      'ip link set upstream2 netns upstream2 2>/dev/null; ip netns exec upstream2 ip link set lo up; ip netns exec upstream2 ip addr replace 198.18.20.2/24 dev upstream2; '
      'ip netns exec upstream2 ip link set upstream2 up; mkdir -p /run/field-r8v; true')
U1 = ('log stderr all; router id 198.18.0.2; protocol device {}\n'
      'protocol static injected { ipv4; route 203.0.113.0/24 blackhole; route 203.0.113.128/25 blackhole; route 0.0.0.0/0 blackhole; route 198.51.100.0/24 blackhole; }\n'
      'protocol bgp dut4 { local 198.18.0.2 as 65001; neighbor 198.18.0.1 as 65000; hold time 9; keepalive time 3; ipv4 { import all; export where source = RTS_STATIC; }; }\n')
U2 = ('log stderr all; router id 198.18.20.2; protocol device {}\n'
      'protocol static injected { ipv4; route 198.51.100.0/24 blackhole; }\n'
      'protocol bgp dut4 { local 198.18.20.2 as 65002; neighbor 198.18.20.1 as 65000; hold time 9; keepalive time 3; ipv4 { import all; export where source = RTS_STATIC; }; }\n')
guest('gateway', 'cat >/root/R8v-u1.conf', data=U1, quiet=True); guest('gateway', 'cat >/root/R8v-u2.conf', data=U2, quiet=True)
guest('gateway', 'bird -c /root/R8v-u1.conf -s /run/field-r8v/u1.ctl -P /run/field-r8v/u1.pid; ip netns exec upstream2 bird -c /root/R8v-u2.conf -s /run/field-r8v/u2.ctl -P /run/field-r8v/u2.pid')
OBS = ('log stderr all; router id 192.168.10.2; protocol device {}\n'
       'protocol bgp dut { local 192.168.10.2 as 65003; neighbor 192.168.10.1 as 65000; hold time 9; keepalive time 3; ipv4 { import all; export none; }; }\n')
guest('client', 'cat >/tmp/R8v-observer.conf', data=OBS, quiet=True)
guest('client', 'kill $(cat /tmp/R8v-observer.pid 2>/dev/null) 2>/dev/null; sleep 1; bird -c /tmp/R8v-observer.conf -s /tmp/R8v-observer.ctl -P /tmp/R8v-observer.pid; '
      'nohup ping -D -i .1 -w 400 198.19.0.1 >/tmp/R8v-forwarding.txt 2>&1 </dev/null &')

# --- DUT policy, native CLI ---
pl = lambda name, rule, prefix: [f'set policy prefix-list {name} rule {rule} action permit', f'set policy prefix-list {name} rule {rule} prefix {prefix}']
rm = lambda name, plist: [f'set policy route-map {name} rule 10 action permit', f'set policy route-map {name} rule 10 match ip address prefix-list {plist}']
cfg = (['set interfaces ethernet eth1 vif 20 address 198.18.20.1/24', 'set protocols static route 192.0.2.0/24 blackhole']
       + pl('CUSTOMER', 10, '203.0.113.0/24') + pl('LOCAL', 10, '192.0.2.0/24') + pl('OBSERVER', 10, '192.0.2.0/24') + pl('OBSERVER', 20, '203.0.113.0/24')
       + rm('CUSTOMER-IN', 'CUSTOMER') + rm('LOCAL-OUT', 'LOCAL') + rm('OBSERVER-OUT', 'OBSERVER')
       + ['set protocols bgp system-as 65000', 'set protocols bgp parameters router-id 198.18.0.1',
          'set protocols bgp address-family ipv4-unicast network 192.0.2.0/24'])
for addr, asn, imp, exp in [('198.18.0.2', 65001, 'CUSTOMER-IN', 'LOCAL-OUT'), ('198.18.20.2', 65002, None, 'LOCAL-OUT'), ('192.168.10.2', 65003, None, 'OBSERVER-OUT')]:
    n = f'set protocols bgp neighbor {addr}'
    cfg += [f'{n} remote-as {asn}', f'{n} timers holdtime 9', f'{n} timers keepalive 3', f'{n} address-family ipv4-unicast route-map export {exp}']
    if imp:
        cfg.append(f'{n} address-family ipv4-unicast route-map import {imp}')
p = vcfg(cfg); out['commit_s'] = round(p.seconds, 2)
time.sleep(25)
dut("vtysh -c 'show bgp ipv4 unicast summary'")
guest('client', 'birdc -s /tmp/R8v-observer.ctl show protocols')
out['dut_rib'] = dut("vtysh -c 'show bgp ipv4 unicast'", quiet=True).stdout
out['transit_advertised'] = dut("vtysh -c 'show bgp ipv4 unicast neighbors 198.18.20.2 advertised-routes'", quiet=True).stdout
out['observer_routes'] = observer_routes()
transit = sorted({l.split()[1] for l in out['transit_advertised'].splitlines() if l.strip().startswith('*')})
out['transit_prefixes'] = transit
bad = [x for x in out['observer_routes'] if x not in ('192.0.2.0/24', '203.0.113.0/24')]
out['unauthorized_at_observer'] = bad
say('observer sees', out['observer_routes'], '| transit gets', transit)

# --- export edit under traffic ---
t0 = time.monotonic(); p = vcfg(['delete policy prefix-list OBSERVER rule 20']); gone = None
while time.monotonic() - t0 < 60:
    if '203.0.113.0/24' not in observer_routes():
        gone = round(time.monotonic() - t0, 2); break
    time.sleep(.3)
out['export_edit'] = {'commit_s': round(p.seconds, 2), 'withdrawn_at_observer_s': gone}
say('export edit: withdrawn at observer after', gone, 's (commit took', round(p.seconds, 2), 's)')

# --- max-prefix 1000 exceeded by one on U1 ---
vcfg(pl('SCALE', 10, '10.64.0.0/16') + ['set policy prefix-list SCALE rule 10 le 32'] + rm('SCALE-IN', 'SCALE')
     + ['set protocols bgp neighbor 198.18.0.2 address-family ipv4-unicast route-map import SCALE-IN',
        'set protocols bgp neighbor 198.18.0.2 address-family ipv4-unicast maximum-prefix 1000'])
subprocess.run(['python3', '/work/field/bgp-prefix-generator.py', '/work/field/R8v-max.conf', '--ipv4', '1001', '--asn', '65001',
                '--local', '198.18.0.2', '--peer', '198.18.0.1'], check=True)
guest('gateway', 'cat >/root/R8v-u1.conf', data=Path('/work/field/R8v-max.conf').read_text(), quiet=True)
t0 = time.monotonic(); guest('gateway', 'birdc -s /run/field-r8v/u1.ctl configure'); rows = []; hit = None
while time.monotonic() - t0 < 30:
    q = dut("vtysh -c 'show bgp neighbors 198.18.0.2 json'", quiet=True, timeout=20)
    try:
        j = json.loads(q.stdout)['198.18.0.2']
        st = {'state': j.get('bgpState'), 'last_reset': j.get('lastResetDueTo'), 'pfx': j.get('addressFamilyInfo', {}).get('ipv4Unicast', {}).get('acceptedPrefixCounter')}
    except Exception:
        st = {'error': q.stdout[-200:]}
    rows.append({'t': round(time.monotonic() - t0, 2), **st})
    if st.get('state') not in (None, 'Established'):
        hit = rows[-1]['t']; break
    time.sleep(.3)
out['max_prefix'] = {'action_s': hit, 'polls': rows[-5:]}
say('max-prefix: session reset after', hit, 's;', json.dumps(rows[-1]))
fwd = guest('client', 'pkill -x ping; sleep 1; tail -3 /tmp/R8v-forwarding.txt', quiet=True).stdout
out['forwarding_tail'] = fwd
(raw / 'R8-vyos.summary.json').write_text(json.dumps(out, indent=2) + '\n')
say('done')
# --- restore ---
guest('gateway', 'pkill bird; true', quiet=True); guest('client', 'kill $(cat /tmp/R8v-observer.pid) 2>/dev/null; true', quiet=True)
vcfg(['delete protocols bgp', 'delete policy', 'delete protocols static route 192.0.2.0/24', 'delete interfaces ethernet eth1 vif 20'], quiet=True)
