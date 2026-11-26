#!/usr/bin/env python3
"""Boot VyOS on the G fabric and apply the base gateway config via serial.

Runs only when no other competitor is up (fabric-competitor.sh enforces).
Configures WAN/LAN/observer, default route, NAT masquerade, SSH with the field
key, and DNS forwarding; commits and saves. Afterwards drive it with
vyos-command (observer SSH) or vyos-command production (through the sink).
"""
import socket, sys, time
from pathlib import Path
from pexpect.fdpexpect import fdspawn

raw = Path('/work/field/raw')
key = Path('/work/field/private/id_ed25519.pub').read_text().split()
key_type, key_body = key[0], key[1]
password = Path('/work/field/private/vyos-password').read_text().strip()

print('booting vyos on fabric', flush=True)
import subprocess
subprocess.run(['bash', '/work/field/fabric-competitor.sh', 'vyos'],
               env={'PATH': '/usr/bin:/bin:/usr/sbin:/sbin', 'INSTALLED': '1'}, check=True)

s = socket.socket(socket.AF_UNIX)
for _ in range(60):
    try:
        s.connect('/work/vms/vyos/serial.sock')
        break
    except OSError:
        time.sleep(2)
c = fdspawn(s, timeout=240, encoding='utf-8', codec_errors='replace')
c.logfile_read = sys.stdout

c.sendline('')
i = c.expect(['login:', r'vyos@vyos:.*\$ ', r'vyos@vyos.*# '])
if i == 0:
    c.sendline('vyos')
    c.expect('Password:')
    c.sendline(password)
    c.expect(r'vyos@vyos:.*\$ ')

commands = [
    'configure',
    'set interfaces ethernet eth0 address 198.18.0.1/24',
    'set interfaces ethernet eth1 address 192.168.10.1/24',
    'set interfaces ethernet eth2 address dhcp',
    'set protocols static route 0.0.0.0/0 next-hop 198.18.0.2',
    'set nat source rule 10 outbound-interface name eth0',
    'set nat source rule 10 source address 192.168.10.0/24',
    'set nat source rule 10 translation address masquerade',
    'set service ssh',
    f'set system login user vyos authentication public-keys field key "{key_body}"',
    f'set system login user vyos authentication public-keys field type {key_type}',
    'set service dns forwarding listen-address 192.168.10.1',
    'set service dns forwarding system',
    'commit',
    'save',
    'exit',
]
for cmd in commands:
    c.sendline(cmd)
    if cmd == 'commit':
        c.expect([r'vyos@vyos.*# ', 'Configuration changes were not applied'], timeout=180)
    elif cmd == 'save':
        c.expect([r'vyos@vyos.*# ', 'Done'], timeout=60)
    else:
        c.expect(r'vyos@vyos.*[#$] ', timeout=60)

print('waiting for observer SSH', flush=True)
import subprocess as sp
for _ in range(60):
    p = sp.run(['/work/field/vyos-command', 'true'], capture_output=True)
    if p.returncode == 0:
        print('vyos observer SSH ok', flush=True)
        break
    time.sleep(3)
else:
    raise SystemExit('vyos observer SSH did not come up')

p = sp.run(['/work/field/vyos-command', 'show version; show interfaces; ip route'],
           capture_output=True, text=True)
(raw / 'vyos-fabric-setup.txt').write_text(p.stdout + p.stderr)
print(p.stdout, flush=True)
(raw / 'vyos-fabric-setup.exit').write_text('0\n')
