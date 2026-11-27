#!/usr/bin/env python3
"""OpenWrt sysupgrade with a power cut a set time (CUT_AFTER) after stage 2 logs
"Sending KILL" (stage 2 does not log the write itself on x86). IMG picks the
image, TAG the output names."""
import json, os, signal, subprocess, time
from pathlib import Path
raw = Path('/work/field/raw'); con = Path('/work/vms/openwrt/console.log')
def say(*a): print(time.strftime('%H:%M:%S', time.gmtime()), *a, flush=True)
def dut(cmd, t=120): return subprocess.run(['/work/field/openwrt-command', cmd], capture_output=True, text=True, timeout=t)
say('$ openwrt-command wget .../' + os.environ.get('IMG', 'openwrt-24.10.7.img.gz') + '; sysupgrade -v /tmp/fw.img.gz')
print(dut('rm -f /tmp/fw.img.gz; wget -q -O /tmp/fw.img.gz http://10.0.2.2:8091/full/' + os.environ.get('IMG', 'openwrt-24.10.7.img.gz') + '; ls -l /tmp/fw.img.gz').stdout, flush=True)
size0 = con.stat().st_size
subprocess.Popen(['/work/field/openwrt-command', 'sysupgrade -v /tmp/fw.img.gz'], stdout=open(raw / ('R2-openwrt-' + os.environ.get('TAG', 'cut2') + '-sysupgrade.txt'), 'w'), stderr=subprocess.STDOUT)
t = time.monotonic(); seen = None
while time.monotonic() - t < 120:
    tail = con.read_bytes()[size0:].decode(errors='replace')
    for key in ('Performing system upgrade', 'Writing image', 'Upgrade completed', 'Sending KILL'):
        if key in tail and seen is None:
            seen = (key, round(time.monotonic() - t, 1))
    if seen:
        break
    time.sleep(.1)
time.sleep(float(os.environ.get('CUT_AFTER', '1.0')))
(raw / ('R2-openwrt-' + os.environ.get('TAG', 'cut2') + '-console-before.txt')).write_bytes(con.read_bytes()[size0:])
pid = int(Path('/work/vms/openwrt/pid').read_text()); argv = [x.decode() for x in Path(f'/proc/{pid}/cmdline').read_bytes().split(b'\0') if x]
os.kill(pid, signal.SIGKILL); cut = time.monotonic(); say(f'$ kill -9 qemu openwrt  # power cut, console showed {seen}')
for _ in range(100):
    if not Path('/work/vms/openwrt/pid').exists(): break
    time.sleep(.1)
Path('/work/vms/openwrt/pid').unlink(missing_ok=True); time.sleep(1); subprocess.run(argv, check=True)
back = None
while time.monotonic() - cut < 300:
    if dut('true', 8).returncode == 0: back = round(time.monotonic() - cut, 1); break
    time.sleep(2)
rel = dut('. /etc/openwrt_release; echo $DISTRIB_RELEASE $DISTRIB_REVISION').stdout.strip() if back else None
fwd = subprocess.run(['/work/field/guest-command', 'client', 'ping -c 3 -W 1 198.19.0.1'], capture_output=True).returncode == 0
time.sleep(5)
(raw / ('R2-openwrt-' + os.environ.get('TAG', 'cut2') + '-console-after.txt')).write_bytes(con.read_bytes()[size0:])
out = {'console_marker_before_cut': seen, 'ssh_back_s': back, 'release_after': rel, 'forwarding_ok': fwd}
(raw / ('R2-openwrt-' + os.environ.get('TAG', 'cut2') + '.summary.json')).write_text(json.dumps(out, indent=2) + '\n'); say('result:', json.dumps(out))
