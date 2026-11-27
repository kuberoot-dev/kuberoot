#!/usr/bin/env python3
"""R3 for VyOS (nearest equivalents): kernel drift, two writers, a bad route
re-applied every 10 s by an automation loop while commit-confirm reverts it.

drift: a configured static route and a configured firewall rule are removed
  from the kernel behind VyOS' back (ip route del / nft delete rule); polled
  for 40 s for repair.
writers: two configuration sessions set the same node to different values and
  commit at the same moment; repeated 5 times.
replay: every 10 s a loop applies `delete protocols static route 0.0.0.0/0`
  with commit-confirm 1 over the production path (it cuts that path off) for
  6 minutes; we record how many reverts happen and whether it settles.
"""
import json, os, subprocess, threading, time
from pathlib import Path

raw = Path('/work/field/raw'); out = {}
PH = os.environ.get('R3_PHASES', 'drift,writers,replay').split(',')
SUM = os.environ.get('R3_SUMMARY', 'R3-vyos.summary.json')


def say(*a):
    print(time.strftime('%H:%M:%S', time.gmtime()), *a, flush=True)


def dut(cmd, quiet=False, timeout=60, production=False):
    if not quiet:
        say('$ vyos-command' + (' production' if production else ''), cmd[:150])
    p = subprocess.run(['/work/field/vyos-command'] + (['production'] if production else []) + [cmd], capture_output=True, text=True, timeout=timeout)
    if not quiet and (p.stdout + p.stderr).strip():
        print((p.stdout + p.stderr).strip()[-500:], flush=True)
    return p


def vscript(lines, commit='commit', production=False, timeout=120):
    s = 'source /opt/vyatta/etc/functions/script-template\nconfigure\n' + '\n'.join(lines) + f'\n{commit}\nexit\n'
    try:
        return subprocess.run(['/work/field/vyos-command'] + (['production'] if production else []) + ['vbash', '-s'],
                              input=s, capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        return subprocess.CompletedProcess([], 124, '', 'client timeout')


def save():
    (raw / SUM).write_text(json.dumps(out, indent=2) + '\n')


if 'drift' in PH:
    # --- drift ---
    say('$ vyos-command vbash -s  # configure; static route 198.51.104.0/24 + firewall forward rule 77; commit')
    p = vscript(['set protocols static route 198.51.104.0/24 next-hop 198.18.0.2',
                 'set firewall ipv4 forward filter rule 77 action drop', 'set firewall ipv4 forward filter rule 77 destination port 9077',
                 'set firewall ipv4 forward filter rule 77 protocol udp', 'set firewall ipv4 forward filter rule 77 description FIELDDRIFT'])
    before = dut("ip route show 198.51.104.0/24; sudo nft -a list ruleset | grep -n FIELDDRIFT")
    dut("sudo ip route del 198.51.104.0/24; sudo nft -a list ruleset | awk '/chain VYOS_FORWARD_filter/{c=1} c && /FIELDDRIFT/{print $NF; exit}' | xargs -I H sudo nft delete rule ip vyos_filter VYOS_FORWARD_filter handle H; echo removed")
    t0 = time.monotonic(); rows = []; route_back = rule_back = None
    while time.monotonic() - t0 < 40:
        q = dut("ip route show 198.51.104.0/24; echo ---; sudo nft list ruleset | grep -c FIELDDRIFT", quiet=True)
        r, _, n = q.stdout.partition('---')
        el = round(time.monotonic() - t0, 1); rows.append({'t': el, 'route': r.strip(), 'rule_count': n.strip()})
        route_back = route_back or (el if r.strip() else None); rule_back = rule_back or (el if n.strip() not in ('', '0') else None)
        time.sleep(2)
    say('drift: route back after', route_back, 's; rule back after', rule_back, 's (None = not repaired in 40 s)')
    out['drift'] = {'before': before.stdout, 'route_repaired_s': route_back, 'rule_repaired_s': rule_back, 'polls': rows}
    save()
    vscript(['delete protocols static route 198.51.104.0/24', 'delete firewall ipv4 forward filter rule 77'])

if 'writers' in PH:
    # --- two writers on the same node ---
    races = []
    for i in range(5):
        res = {}
        def writer(name, value):
            t = time.monotonic(); p = vscript([f'set firewall ipv4 forward filter rule 78 description {value}', 'set firewall ipv4 forward filter rule 78 action accept'])
            res[name] = {'exit': p.returncode, 'seconds': round(time.monotonic() - t, 2), 'tail': (p.stdout + p.stderr).strip().splitlines()[-3:]}
        ts = [threading.Thread(target=writer, args=(n, f'writer-{n}-{i}')) for n in ('a', 'b')]
        [t.start() for t in ts]; [t.join() for t in ts]
        final = dut("/opt/vyatta/bin/vyatta-op-cmd-wrapper show configuration commands | grep 'rule 78 description'", quiet=True).stdout.strip()
        res['final'] = final; races.append(res); say(f'race {i}:', json.dumps(res)[:400])
    out['writers'] = races; save()
    vscript(['delete firewall ipv4 forward filter rule 78'])

if 'replay' in PH:
    # --- replay of a bad change against commit-confirm ---
    say('== replay: every 10 s `delete protocols static route 0.0.0.0/0; commit-confirm 1` over the production path, 6 min')
    log = []; t0 = time.monotonic()
    while time.monotonic() - t0 < 360:
        t = time.monotonic()
        p = vscript(['delete protocols static route 0.0.0.0/0'], commit='commit-confirm 1\ny', production=True, timeout=9)
        st = dut("/opt/vyatta/bin/vyatta-op-cmd-wrapper show configuration commands | grep -c 'static route 0.0.0.0/0'; uptime -s", quiet=True)
        log.append({'t': round(t - t0, 1), 'apply_exit': p.returncode, 'apply_tail': (p.stdout + p.stderr).strip().splitlines()[-2:],
                    'default_route_lines': st.stdout.split()[0] if st.stdout.split() else None, 'boot': ' '.join(st.stdout.split()[1:3])})
        if len(log) % 6 == 0:
            say(f'replay {len(log)} attempts: last', json.dumps(log[-1])[:300])
        time.sleep(max(0, 10 - (time.monotonic() - t)))
    out['replay'] = {'attempts': len(log), 'accepted': sum(1 for r in log if r['apply_exit'] == 0),
                     'boots_seen': sorted({r['boot'] for r in log}), 'log': log}
    save()
    # let any pending commit-confirm run out, then check the default route is back
    wait0 = time.monotonic(); back = None
    while time.monotonic() - wait0 < 240:
        st = dut("/opt/vyatta/bin/vyatta-op-cmd-wrapper show configuration commands | grep -c 'static route 0.0.0.0/0'", quiet=True)
        if st.returncode == 0 and st.stdout.strip() not in ('', '0') and dut('ping -c1 -W1 198.19.0.1', quiet=True).returncode == 0:
            back = round(time.monotonic() - wait0, 1); break
        time.sleep(5)
    out['replay']['settled_after_loop_s'] = back; save()
    say('replay:', json.dumps({k: v for k, v in out['replay'].items() if k != 'log'}))
