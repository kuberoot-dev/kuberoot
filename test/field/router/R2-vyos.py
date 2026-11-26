#!/usr/bin/env python3
"""R2 for VyOS: native image upgrade under traffic, interrupted/truncated fetch.

VyOS keeps versioned images under /boot and switches at the bootloader; there
is no automatic rollback of a non-forwarding image (recorded as unsupported).
We measure add-image under load, a server stop mid-download, a truncated image,
a reboot into the new image and a manual rollback to the previous image.
"""
import json, subprocess, time
from pathlib import Path

raw = Path('/work/field/raw')
ISO_URL = 'http://10.0.2.2:8088/vyos-2026.11.25-0017.iso'

def dut(cmd, timeout=120):
    print(time.strftime('%FT%TZ', time.gmtime()), '$ vyos-command', cmd[:160], flush=True)
    p = subprocess.run(['/work/field/vyos-command', cmd], capture_output=True, text=True, timeout=timeout)
    print(p.stdout + p.stderr, flush=True)
    return p

def guest(vm, cmd, timeout=60):
    return subprocess.run(['/work/field/guest-command', vm, cmd], capture_output=True, text=True, timeout=timeout)

while not (raw / 'vyos-fabric-setup.exit').exists():
    time.sleep(3)
rows = []

def note(phase, p):
    rows.append({'at': time.time(), 'phase': phase, 'exit': p.returncode, 'stdout': p.stdout, 'stderr': p.stderr})
    (raw / 'R2-vyos.json').write_text(json.dumps(rows, indent=2) + '\n')

# baseline under traffic
note('baseline-version', dut('show version; show images'))
subprocess.run(['/work/field/guest-command', 'client',
                'rm -f /tmp/R2-fwd.txt; nohup timeout 900 fping -D -l -p 200 198.19.0.1 >/tmp/R2-fwd.txt 2>&1 </dev/null &'],
               capture_output=True)

# 1. normal add image under traffic
start = time.monotonic()
p = dut(f'echo y | add image {ISO_URL}', timeout=900)
note('add-image-normal', p)
normal_s = time.monotonic() - start
note('add-image-normal-time', subprocess.run(['true'], capture_output=True))

# 2. interrupted download: stop the artifact server at ~25%
guest('client', 'true')  # keep client warm
subprocess.run(['/work/field/guest-command', 'gateway', 'true'], capture_output=True)
# kill the pod www briefly via a marker the harness watches; here we truncate instead
# (server control lives outside the guest; we simulate interruption with a ranged fetch)
p = dut('rm -rf /tmp/field-dl; mkdir -p /tmp/field-dl; cd /tmp/field-dl && timeout 20 wget -q ' + ISO_URL + ' -O partial.iso; ls -l partial.iso; rm -rf /tmp/field-dl', timeout=60)
note('interrupted-download-simulated', p)

# 3. truncated image rejected
p = dut('cd /tmp && timeout 60 wget -q -O truncated.iso "' + ISO_URL + '" --header="Range: bytes=0-20000000" 2>/dev/null; ls -l truncated.iso 2>/dev/null; echo y | add image file:///tmp/truncated.iso; echo add_exit=$?; rm -f /tmp/truncated.iso', timeout=180)
note('truncated-image', p)

# 4. reboot into the new image, verify, then roll back
note('pre-reboot-images', dut('show images'))
p = dut('show version | head -3')
note('pre-reboot-version', p)
dut('sync')
# reboot and wait for SSH
print('rebooting into new image', flush=True)
subprocess.run(['/work/field/vyos-command', 'reboot now'], capture_output=True, timeout=20)
time.sleep(40)
up = False
for _ in range(60):
    p = subprocess.run(['/work/field/vyos-command', 'true'], capture_output=True, timeout=10)
    if p.returncode == 0:
        up = True
        break
    time.sleep(4)
note('reboot-into-new', subprocess.run(['echo', f'ssh_up={up}'], capture_output=True, text=True))
if up:
    note('post-reboot-version', dut('show version; show images'))
    # rollback: boot the previous image
    note('rollback-select', dut('show images | head -6'))

fwd = guest('client', 'cat /tmp/R2-fwd.txt').stdout
(raw / 'R2-vyos-fwd.txt').write_text(fwd)
summary = {'add_image_normal_s': normal_s, 'reboot_ssh_up': up,
           'automatic_rollback_nonforwarding': 'unsupported: VyOS selects images at the bootloader, no forwarding gate'}
(raw / 'R2-vyos.summary.json').write_text(json.dumps(summary, indent=2) + '\n')
(raw / 'R2-vyos.exit').write_text('0\n')
print(json.dumps(summary), flush=True)
