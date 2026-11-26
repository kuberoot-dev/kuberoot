#!/usr/bin/env python3
"""Replay the observed native installer, exclusively in the field VyOS VM.

Requires a fresh live ISO guest and its own 12 GiB /work/vms/vyos/sys.qcow2.
Destroys only that guest's /dev/vda after the native installer lists it.
"""
import socket,sys,secrets
from pathlib import Path
from pexpect.fdpexpect import fdspawn
if not Path('/work/vms/vyos/sys.qcow2').exists():raise SystemExit('field disk missing')
p=Path('/work/field/private/vyos-password');p.parent.mkdir(mode=0o700,exist_ok=True)
if not p.exists():p.write_text(secrets.token_urlsafe(24));p.chmod(0o600)
s=socket.socket(socket.AF_UNIX);s.connect('/work/vms/vyos/serial.sock')
c=fdspawn(s,timeout=180,encoding='utf-8',codec_errors='replace');c.logfile_read=sys.stdout
c.sendline('');i=c.expect(['login:',r'vyos@vyos:.*\$ '])
if i==0:
 c.sendline('vyos');c.expect('Password:');c.sendline('vyos');c.expect(r'vyos@vyos:.*\$ ')
c.sendline('install image')
steps=[
 ('Would you like to continue?', 'y'),
 ('What would you like to name this image?', ''),
 ('Please enter a password for the "vyos" user:', p.read_text()),
 ('Please confirm password for the "vyos" user:', p.read_text()),
 ('What console should be used by default?', 'S'),
 ('Drive: /dev/vda (12.0 GB)', None),
 ('Which one should be used for installation?', '/dev/vda'),
 ('Installation will delete all data on the drive. Continue?', 'y'),
 ('Would you like to use all the free space on the drive?', 'y'),
 ('Which file would you like as boot config?', '2'),
 ('The image installed successfully; please reboot now.', None)]
for prompt,answer in steps:
 c.expect_exact(prompt)
 if answer is not None:c.sendline(answer)
print('\nNative installer completed. Restart without the ISO using INSTALLED=1.')
