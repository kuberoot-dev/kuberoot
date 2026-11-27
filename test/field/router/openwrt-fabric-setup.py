#!/usr/bin/env python3
"""Boot stock OpenWrt on the G fabric and apply the base gateway config via serial.

Configures LAN eth1 192.168.10.1/24 with DHCP, WAN eth0 198.18.0.1/24 via the
site gateway, an observer interface on eth2 (user-net, management only), root
SSH with the field key, and dnsmasq. firewall4 keeps its native wan masquerade.
Afterwards drive it with openwrt-command (observer) or openwrt-command production.
"""
import json, subprocess, time
from pathlib import Path

raw = Path('/work/field/raw')
key = Path('/work/field/private/id_ed25519.pub').read_text().strip()
import secrets
pwf = Path('/work/field/private/openwrt-root-password')
if not pwf.exists():
    pwf.write_text(secrets.token_urlsafe(18)); pwf.chmod(0o600)
pw = pwf.read_text().strip()

print('booting openwrt on fabric', flush=True)
subprocess.run(['bash', '/work/field/fabric-competitor.sh', 'openwrt'],
               env={'PATH': '/usr/bin:/bin:/usr/sbin:/sbin'}, check=True)
time.sleep(35)  # first boot: overlay format + rc

setup = r'''
uci set network.lan.device='eth1'
uci set network.lan.ipaddr='192.168.10.1'
uci set network.lan.netmask='255.255.255.0'
uci set network.wan.device='eth0'
uci set network.wan.proto='static'
uci set network.wan.ipaddr='198.18.0.1'
uci set network.wan.netmask='255.255.255.0'
uci set network.wan.gateway='198.18.0.2'
uci set network.wan.dns='198.18.0.2'
uci set network.observer=interface
uci set network.observer.device='eth2'
uci set network.observer.proto='dhcp'
uci set network.observer.defaultroute='0'
uci set network.observer.peerdns='0'
uci set firewall.observer=zone
uci set firewall.observer.name='observer'
uci set firewall.observer.network='observer'
uci set firewall.observer.input='ACCEPT'
uci set firewall.observer.output='ACCEPT'
uci set firewall.observer.forward='REJECT'
uci set firewall.field_ssh_wan=rule
uci set firewall.field_ssh_wan.name='field-ssh-wan'
uci set firewall.field_ssh_wan.src='wan'
uci set firewall.field_ssh_wan.proto='tcp'
uci set firewall.field_ssh_wan.dest_port='22'
uci set firewall.field_ssh_wan.target='ACCEPT'
printf 'PWPLACEHOLDER\nPWPLACEHOLDER\n' | passwd root
uci set dhcp.lan.start='100'
uci set dhcp.lan.limit='100'
uci commit network
uci commit dhcp
uci commit firewall
mkdir -p /etc/dropbear
echo 'KEYPLACEHOLDER' >> /etc/dropbear/authorized_keys
chmod 600 /etc/dropbear/authorized_keys
/etc/init.d/network restart
sleep 5
/etc/init.d/dnsmasq restart
/etc/init.d/firewall restart
/etc/init.d/dropbear restart
'''.replace('KEYPLACEHOLDER', key).replace('PWPLACEHOLDER', pw)

print('applying base config via serial', flush=True)
p = subprocess.run(['python3', '/work/field/console.py', 'openwrt', setup, '--timeout', '120'],
                   capture_output=True, text=True, timeout=150)
print((p.stdout + p.stderr).replace(pw, '***'), flush=True)
(raw / 'openwrt-fabric-setup-serial.txt').write_text((p.stdout + p.stderr).replace(pw, '***'))

print('waiting for observer SSH', flush=True)
for _ in range(60):
    p = subprocess.run(['/work/field/openwrt-command', 'true'], capture_output=True)
    if p.returncode == 0:
        print('openwrt observer SSH ok', flush=True)
        break
    time.sleep(3)
else:
    raise SystemExit('openwrt observer SSH did not come up')

p = subprocess.run(['/work/field/openwrt-command', 'cat /etc/openwrt_release; ip -br addr; ip route; uci show network | head -40'],
                   capture_output=True, text=True)
(raw / 'openwrt-fabric-setup.txt').write_text(p.stdout + p.stderr)
print(p.stdout, flush=True)
(raw / 'openwrt-fabric-setup.exit').write_text('0\n')
