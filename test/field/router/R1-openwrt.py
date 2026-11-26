#!/usr/bin/env python3
"""R1 for OpenWrt: native rpcd UCI apply-with-rollback and automatic revert.

Each phase stages a management-locking UCI change and applies it through the
native rpcd path `ubus call uci apply '{"timeout":60}'` over the production WAN
SSH (root@198.18.0.1 via the sink). No `ubus call uci confirm` is sent, so rpcd
must roll the change back after the timeout. We measure management loss ->
automatic recovery through the same production path; the isolated observer NIC
is never used for repair. Persistence of the rollback across the apply daemon
is observed, not assumed.
"""
import json, subprocess, time
from pathlib import Path

raw = Path('/work/field/raw')

def production(cmd, timeout=12):
    return subprocess.run(['ssh', '-F', '/work/field/private/production-ssh.conf', 'openwrt-production', cmd],
                          capture_output=True, text=True, timeout=timeout)

def observer(cmd, timeout=15):
    return subprocess.run(['/work/field/openwrt-command', cmd], capture_output=True, text=True, timeout=timeout)

def inject(uci_lines):
    # Stage the change, then apply with a 60 s rollback timer via native rpcd.
    script = '\n'.join(uci_lines) + "\nubus call uci apply '{\"timeout\":60}'\n"
    print('$ ssh openwrt-production (uci stage + rpcd apply timeout=60)', flush=True)
    print(script, flush=True)
    try:
        p = subprocess.run(['ssh', '-F', '/work/field/private/production-ssh.conf', 'openwrt-production'],
                           input=script, capture_output=True, text=True, timeout=25)
        print(p.stdout + p.stderr, flush=True)
        return p.returncode
    except subprocess.TimeoutExpired:
        print('(session severed by the lockout, as designed)', flush=True)
        return None

phases = [
    ('address',
     ["uci set network.wan.ipaddr='198.18.0.99'", "uci commit network"],
     'wan back to 198.18.0.1'),
    ('return-route',
     ["uci delete network.wan.gateway", "uci commit network"],
     'default gateway restored'),
    ('vlan',
     ["uci set network.wan.device='eth0.999'", "uci commit network"],
     'untagged wan device restored'),
    ('firewall',
     ["uci add firewall rule", "uci set firewall.@rule[-1].name='FIELDLOCK'",
      "uci set firewall.@rule[-1].src='wan'", "uci set firewall.@rule[-1].proto='tcp'",
      "uci set firewall.@rule[-1].dest_port='22'", "uci set firewall.@rule[-1].target='DROP'",
      "uci commit firewall"],
     'fieldlock rule removed'),
]

for phase, uci_lines, expect in phases:
    p = production('date -u; echo "$SSH_CONNECTION"; ip -br addr show dev eth0 2>/dev/null || ip addr show dev eth0')
    if p.returncode:
        raise SystemExit(f'{phase}: positive production control failed: {p.stderr}')
    print(time.strftime('%FT%TZ', time.gmtime()), phase, 'production control:', p.stdout.strip()[:200], flush=True)
    subprocess.run(['/work/field/guest-command', 'client',
                    'rm -f /tmp/R1-fwd.txt; nohup timeout 260 fping -D -l -p 500 198.19.0.1 >/tmp/R1-fwd.txt 2>&1 </dev/null &'],
                   capture_output=True)
    start = time.monotonic()
    inject(uci_lines)
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
        if ok and lost_at is not None and recovered_at is not None:
            pass
        if ok and lost_at is not None:
            recovered_at = el
            break
        time.sleep(3)
    state = None
    if recovered_at is not None:
        p = production('uci show network.wan; uci show firewall | grep -i fieldlock; ip addr show dev eth0', timeout=15)
        state = p.stdout
    fwd = subprocess.run(['/work/field/guest-command', 'client', 'cat /tmp/R1-fwd.txt'],
                         capture_output=True, text=True, timeout=20).stdout
    (raw / f'R1-openwrt-{phase}-fwd.txt').write_text(fwd)
    summary = {'phase': phase, 'native_mechanism': "rpcd uci apply timeout=60, no confirm",
               'management_lost_s': lost_at, 'automatic_recovery_s': recovered_at,
               'no_observer_repair': True, 'config_state_after': state, 'probes': rows}
    (raw / f'R1-openwrt-{phase}.json').write_text(json.dumps(summary, indent=2) + '\n')
    print(json.dumps({k: v for k, v in summary.items() if k not in ('probes', 'config_state_after')}), flush=True)
    time.sleep(15)

(raw / 'R1-openwrt.exit').write_text('0\n')
