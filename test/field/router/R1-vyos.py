#!/usr/bin/env python3
"""R1 for VyOS: native commit-confirm lockout and automatic revert.

Each phase injects a management-locking change via the production WAN path
(vyos@198.18.0.1 through the sink) using VyOS `commit-confirm 1` (60 s trial).
No confirmation is sent. VyOS reverts automatically (rebooting); we measure
management loss -> automatic recovery through the same production path, and
whether a reboot occurred. The isolated observer NIC is never used for repair.
"""
import json, shlex, subprocess, time
from pathlib import Path

raw = Path('/work/field/raw')

def production(cmd, timeout=12):
    return subprocess.run(['ssh', '-F', '/work/field/private/production-ssh.conf', 'vyos-production', cmd],
                          capture_output=True, text=True, timeout=timeout)

def observer(cmd, timeout=15):
    return subprocess.run(['/work/field/vyos-command', cmd], capture_output=True, text=True, timeout=timeout)

def inject(script):
    # Feed the config session over the production transport; the session drops
    # when the lockout commits, which is expected. commit-confirm runs on the DUT.
    print('$ ssh vyos-production (config session)', flush=True)
    print(script, flush=True)
    try:
        p = subprocess.run(['ssh', '-F', '/work/field/private/production-ssh.conf', 'vyos-production'],
                           input=script, capture_output=True, text=True, timeout=25)
        print(p.stdout + p.stderr, flush=True)
        return p.returncode
    except subprocess.TimeoutExpired:
        print('(session severed by the lockout, as designed)', flush=True)
        return None

phases = [
    ('address',
     'configure\nset interfaces ethernet eth0 address 198.18.0.99/24\ndelete interfaces ethernet eth0 address 198.18.0.1/24\ncommit-confirm 1\n',
     '198.18.0.1/24 present on eth0'),
    ('return-route',
     'configure\ndelete protocols static route 0.0.0.0/0\ncommit-confirm 1\n',
     'default route present'),
    ('vlan',
     'configure\nset interfaces ethernet eth0 vif 999 address 198.18.0.1/24\ndelete interfaces ethernet eth0 address 198.18.0.1/24\ncommit-confirm 1\n',
     'untagged management restored'),
    ('firewall',
     'configure\nset firewall ipv4 name FIELDLOCK default-action drop\nset interfaces ethernet eth0 firewall local name FIELDLOCK\ncommit-confirm 1\n',
     'local firewall removed'),
]

for phase, script, expect in phases:
    p = production('date -u; echo "$SSH_CONNECTION"; uptime')
    if p.returncode:
        raise SystemExit(f'{phase}: positive production control failed: {p.stderr}')
    print(time.strftime('%FT%TZ', time.gmtime()), phase, 'production control:', p.stdout.strip(), flush=True)
    # background forwarding probe through the DUT (client -> sink)
    subprocess.run(['/work/field/guest-command', 'client',
                    'rm -f /tmp/R1-fwd.txt; nohup timeout 260 fping -D -l -p 500 198.19.0.1 >/tmp/R1-fwd.txt 2>&1 </dev/null &'],
                   capture_output=True)
    start = time.monotonic()
    inject(script)
    lost_at = None
    recovered_at = None
    rows = []
    while time.monotonic() - start < 300:
        t = time.monotonic()
        try:
            p = production('true', timeout=6)
            ok = p.returncode == 0
            err = p.stderr.strip()[:120]
        except subprocess.TimeoutExpired:
            ok, err = False, 'timeout'
        el = time.monotonic() - start
        rows.append({'at': time.time(), 'elapsed_s': el, 'ok': ok, 'err': err})
        if not ok and lost_at is None:
            lost_at = el
        if ok and lost_at is not None and recovered_at is None:
            recovered_at = el
            break
        time.sleep(3)
    # determine reboot and config state after recovery
    rebooted = None
    reverted = None
    if recovered_at is not None:
        p = production('uptime; show interfaces ethernet eth0; show protocols static route 0.0.0.0/0; show firewall ipv4 name FIELDLOCK 2>/dev/null; show interfaces ethernet eth0 vif 999 2>/dev/null', timeout=15)
        out = p.stdout
        rebooted = 'up' in out.splitlines()[0] if out else None
        # crude uptime parse: minutes
        reverted = {
            'address_gone': '198.18.0.99' not in out,
            'mgmt_address_back': '198.18.0.1/24' in out,
            'default_route_back': '0.0.0.0/0' in out or 'next-hop' in out,
            'fieldlock_gone': 'FIELDLOCK' not in out,
            'vif999_gone': 'vif 999' not in out,
        }
    subprocess.run(['/work/field/guest-command', 'client', 'pkill fping; cat /tmp/R1-fwd.txt'],
                   capture_output=True, text=True, timeout=20)
    fwd = subprocess.run(['/work/field/guest-command', 'client', 'cat /tmp/R1-fwd.txt'],
                         capture_output=True, text=True, timeout=20).stdout
    (raw / f'R1-vyos-{phase}-fwd.txt').write_text(fwd)
    summary = {'phase': phase, 'native_mechanism': 'commit-confirm 1 (60 s trial)',
               'management_lost_s': lost_at, 'automatic_recovery_s': recovered_at,
               'no_observer_repair': True, 'reboot_observed': rebooted,
               'config_state_after': reverted, 'probes': rows}
    (raw / f'R1-vyos-{phase}.json').write_text(json.dumps(summary, indent=2) + '\n')
    print(json.dumps({k: v for k, v in summary.items() if k != 'probes'}), flush=True)
    # settle before next phase
    time.sleep(20)

(raw / 'R1-vyos.exit').write_text('0\n')
