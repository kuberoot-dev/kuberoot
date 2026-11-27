#!/usr/bin/env python3
"""R3 for VyOS, firewall part of the drift check: a configured forward rule
is deleted from the kernel's nftables behind VyOS' back; polled 40 s.
VyOS names its rules in nft comments as ipv4-FWD-filter-<n>."""
import json, subprocess, time
from pathlib import Path

raw = Path('/work/field/raw')


def say(*a):
    print(time.strftime('%H:%M:%S', time.gmtime()), *a, flush=True)


def dut(cmd, quiet=False):
    if not quiet:
        say('$ vyos-command', cmd[:160])
    p = subprocess.run(['/work/field/vyos-command', cmd], capture_output=True, text=True, timeout=60)
    if not quiet and (p.stdout + p.stderr).strip():
        print((p.stdout + p.stderr).strip()[-600:], flush=True)
    return p


def vcfg(lines):
    s = 'source /opt/vyatta/etc/functions/script-template\nconfigure\n' + '\n'.join(lines) + '\ncommit\nexit\n'
    say('$ vyos-command vbash -s  # configure;', '; '.join(lines)[:160], '; commit')
    return subprocess.run(['/work/field/vyos-command', 'vbash', '-s'], input=s, capture_output=True, text=True, timeout=300)


vcfg(['set firewall ipv4 forward filter rule 77 action drop', 'set firewall ipv4 forward filter rule 77 destination port 9077',
      'set firewall ipv4 forward filter rule 77 protocol udp'])
before = dut("sudo nft -a list chain ip vyos_filter VYOS_FORWARD_filter | grep -n 'dport 9077'")
dut("sudo nft -a list chain ip vyos_filter VYOS_FORWARD_filter | awk '/dport 9077/{print $NF}' | xargs -I H sudo nft delete rule ip vyos_filter VYOS_FORWARD_filter handle H; "
    "sudo nft list chain ip vyos_filter VYOS_FORWARD_filter | grep -c 'dport 9077'")
t0 = time.monotonic(); back = None; polls = []
while time.monotonic() - t0 < 40:
    n = dut("sudo nft list chain ip vyos_filter VYOS_FORWARD_filter | grep -c 'dport 9077'", quiet=True).stdout.strip()
    polls.append({'t': round(time.monotonic() - t0, 1), 'rules': n})
    if n not in ('', '0'):
        back = polls[-1]['t']; break
    time.sleep(2)
say('rule back after', back, 's (None = not repaired in 40 s)')
p = dut("/opt/vyatta/bin/vyatta-op-cmd-wrapper show configuration commands | grep 'forward filter rule 77'")
out = {'before': before.stdout, 'rule_repaired_s': back, 'polls': polls, 'config_still_has_rule': '77' in p.stdout}
(raw / 'R3-vyos-rule-drift.json').write_text(json.dumps(out, indent=2) + '\n')
vcfg(['delete firewall ipv4 forward filter rule 77'])
