#!/usr/bin/env python3
"""R2 for VyOS: native image upgrade under traffic, with download and power faults.

Images: running 2026.11.25-0017 (pinned), target 2026.11.26-0016 (pinned by
SHA256 in raw/R2-vyos-images.json), served from the pod by fault-image-server.py
(port 8091) to VyOS' observer link (10.0.2.2). The client pings the sink
through VyOS every 100 ms for the whole run.

1 download stops at 25 % (server closes the connection)
2 truncated image (a complete, short file)
3 QEMU killed (power cut) while the new image is being written
4 normal add image under traffic, then reboot into it (default boot)
5 power cut right after boot selection (during the first boot of the new image)
6 rollback: previous image as default boot, reboot
Crash and non-forwarding builds: VyOS has no health-gated promotion or
automatic fallback; recorded as unsupported, not built.
"""
import json, os, signal, subprocess, time
from pathlib import Path

raw = Path('/work/field/raw'); out = {}; events = []
NEW, OLD = '2026.11.26-0016-rolling', '2026.11.25-0017-rolling'  # image names as VyOS lists them
URL = 'http://10.0.2.2:8091/{mode}/vyos-2026.11.26-0016.iso'
INSTALLER = 'sudo /usr/libexec/vyos/op_mode/image_installer.py --action add --no-prompt --image-path '


def say(*a):
    print(time.strftime('%H:%M:%S', time.gmtime()), *a, flush=True)


def dut(cmd, quiet=False, timeout=60):
    if not quiet:
        say('$ vyos-command', cmd[:160])
    try:
        p = subprocess.run(['/work/field/vyos-command', cmd], capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        p = subprocess.CompletedProcess([], 124, '', 'client timeout')
    if not quiet and (p.stdout + p.stderr).strip():
        print((p.stdout + p.stderr).strip()[-700:], flush=True)
    return p


def op(cmd, quiet=False, timeout=60):
    # op-mode commands over a non-interactive SSH session go through the wrapper
    return dut(f'/opt/vyatta/bin/vyatta-op-cmd-wrapper {cmd}', quiet, timeout)


def images():
    p = op('show system image', quiet=True)
    return p.stdout.strip()


def ev(phase, **kw):
    events.append({'at': time.time(), 'phase': phase, **kw})
    (raw / 'R2-vyos-events.json').write_text(json.dumps(events, indent=2) + '\n')
    (raw / 'R2-vyos.summary.json').write_text(json.dumps(out, indent=2) + '\n')


def pid():
    return int(Path('/work/vms/vyos/pid').read_text())


def argv():
    return [x.decode() for x in Path(f'/proc/{pid()}/cmdline').read_bytes().split(b'\0') if x]


def power_cut(args):
    p = pid(); os.kill(p, signal.SIGKILL); say('$ kill -9 qemu vyos  # power cut')
    for _ in range(100):
        if not Path('/work/vms/vyos/pid').exists() or Path(f'/proc/{p}/stat').read_text().split(')')[1].split()[0] == 'Z':
            break
        time.sleep(.1)
    Path('/work/vms/vyos/pid').unlink(missing_ok=True)
    for _ in range(20):
        if subprocess.run(args, capture_output=True).returncode == 0:
            return time.monotonic()
        time.sleep(1)
    raise SystemExit('vyos did not restart')


def wait_ssh(limit=300):
    t = time.monotonic()
    while time.monotonic() - t < limit:
        if dut('true', quiet=True, timeout=10).returncode == 0:
            return round(time.monotonic() - t, 1)
        time.sleep(3)


def wait_forwarding(t0, limit=300):
    while time.monotonic() - t0 < limit:
        if subprocess.run(['/work/field/guest-command', 'client', 'ping -c1 -W1 198.19.0.1'], capture_output=True).returncode == 0:
            return round(time.monotonic() - t0, 1)
        time.sleep(1)


def version():
    return op('show version', quiet=True).stdout


def running():
    v = version(); return next((l.split(':', 1)[1].strip() for l in v.splitlines() if l.startswith('Version')), None)


say('installer:', dut('sudo /usr/libexec/vyos/op_mode/image_installer.py --help | head -30', quiet=True).stdout[-900:])
out['baseline'] = {'running': running(), 'images': images()}
say('running', out['baseline']['running']); print(out['baseline']['images'], flush=True)
subprocess.run(['/work/field/guest-command', 'client', 'rm -f /tmp/R2v-fwd.txt; nohup timeout 3000 fping -D -l -p 100 198.19.0.1 >/tmp/R2v-fwd.txt 2>&1 </dev/null &'])
time.sleep(5)

# 1 interrupted download
ev('download-disconnect'); t = time.monotonic(); p = dut(INSTALLER + URL.format(mode='disconnect'), timeout=600)
out['download_disconnect'] = {'exit': p.returncode, 'seconds': round(time.monotonic() - t, 1), 'tail': (p.stdout + p.stderr).strip().splitlines()[-4:],
                              'images_after': images(), 'running_after': running()}
ev('download-disconnect-done')
# 2 truncated image
ev('truncated'); t = time.monotonic(); p = dut(INSTALLER + URL.format(mode='truncated'), timeout=600)
out['truncated'] = {'exit': p.returncode, 'seconds': round(time.monotonic() - t, 1), 'tail': (p.stdout + p.stderr).strip().splitlines()[-4:],
                    'images_after': images(), 'running_after': running()}
ev('truncated-done')

# 3 power cut while the image is written
ev('cut-during-write'); args = argv()
proc = subprocess.Popen(['/work/field/vyos-command', INSTALLER + URL.format(mode='full')], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
t = time.monotonic(); cut_at = None
while time.monotonic() - t < 600:
    q = dut(f'ls -d /usr/lib/live/mount/persistence/boot/{NEW} 2>/dev/null; df -m /usr/lib/live/mount/persistence | tail -1', quiet=True, timeout=15)
    if NEW in q.stdout:
        time.sleep(3); cut_at = round(time.monotonic() - t, 1); break
    time.sleep(1)
t_cut = power_cut(args); proc.kill()
ssh = wait_ssh(); fwd = wait_forwarding(t_cut)
out['cut_during_write'] = {'cut_after_s': cut_at, 'ssh_back_s': ssh, 'forwarding_back_s': fwd, 'running_after': running(), 'images_after': images()}
say('cut during write:', json.dumps(out['cut_during_write'])[:500])
ev('cut-during-write-done')
# remove a half-written image if one is listed, so the normal install starts clean
if NEW in out['cut_during_write']['images_after']:
    op(f'delete system image {NEW}', timeout=120)

# 4 normal install under traffic, reboot into it
ev('install'); t = time.monotonic(); p = dut(INSTALLER + URL.format(mode='full'), timeout=1200)
out['install'] = {'exit': p.returncode, 'seconds': round(time.monotonic() - t, 1), 'tail': (p.stdout + p.stderr).strip().splitlines()[-4:], 'images_after': images()}
say('install:', json.dumps(out['install'])[:500]); ev('install-done')
ev('reboot-new'); dut('sudo systemctl reboot', quiet=True, timeout=10); t = time.monotonic(); time.sleep(5)
ssh = wait_ssh(); fwd = wait_forwarding(t)
out['reboot_new'] = {'ssh_back_s': round(time.monotonic() - t, 1) if ssh is not None else None, 'forwarding_back_s': fwd, 'running_after': running(),
                     'config_kept': 'masquerade' in op("show configuration commands", quiet=True).stdout}
say('reboot into new:', json.dumps(out['reboot_new'])); ev('reboot-new-done')

# 5 power cut during the next boot (right after the boot loader chose the image)
ev('cut-after-selection'); args = argv(); dut('sudo systemctl reboot', quiet=True, timeout=10)
t = time.monotonic()
while time.monotonic() - t < 120:   # wait until the guest has reset (console shows the boot loader again)
    if 'GRUB' in Path('/work/vms/vyos/console.log').read_bytes()[-20000:].decode(errors='replace') or time.monotonic() - t > 20:
        break
    time.sleep(.5)
time.sleep(4); t_cut = power_cut(args); ssh = wait_ssh(); fwd = wait_forwarding(t_cut)
out['cut_after_selection'] = {'ssh_back_s': ssh, 'forwarding_back_s': fwd, 'running_after': running()}
say('cut after selection:', json.dumps(out['cut_after_selection'])); ev('cut-after-selection-done')

# 6 rollback to the previous image
ev('rollback'); op(f'set system image default-boot {OLD}', timeout=60); dut('sudo systemctl reboot', quiet=True, timeout=10)
t = time.monotonic(); time.sleep(5); ssh = wait_ssh(); fwd = wait_forwarding(t)
out['rollback'] = {'ssh_back_s': round(time.monotonic() - t, 1) if ssh is not None else None, 'forwarding_back_s': fwd, 'running_after': running(), 'images_after': images()}
say('rollback:', json.dumps(out['rollback'])); ev('rollback-done')
out['crash_and_noforward_builds'] = 'unsupported: no health-gated promotion or automatic fallback in VyOS image management'

# --- forwarding gaps per phase ---
subprocess.run(['/work/field/guest-command', 'client', 'pkill fping'], capture_output=True); time.sleep(1)
data = subprocess.run(['/work/field/guest-command', 'client', 'cat /tmp/R2v-fwd.txt'], capture_output=True, text=True).stdout
(raw / 'R2-vyos-fwd.txt').write_text(data)
skew = float(subprocess.run(['/work/field/guest-command', 'client', 'date +%s.%N'], capture_output=True, text=True).stdout) - time.time()
rep = [float(l[1:l.index(']')]) - skew for l in data.splitlines() if l.startswith('[') and 'bytes' in l]
gaps = [(a, b - a) for a, b in zip(rep, rep[1:])]
at = {e['phase']: e['at'] for e in events}
for name in ('download-disconnect', 'truncated', 'cut-during-write', 'install', 'reboot-new', 'cut-after-selection', 'rollback'):
    a, b = at[name], at[name + '-done']
    g = [d for t, d in gaps if a - 1 <= t <= b]
    out.setdefault('forwarding_max_gap_s', {})[name] = round(max(g), 2) if g else None
ev('end'); say('gaps:', json.dumps(out['forwarding_max_gap_s']))
