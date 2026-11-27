#!/usr/bin/env python3
"""Check a test build on r11 (root-a rewritten with $FIELD_BUILD, default
/work/field7/out; see build-test-build.sh): phases boot, iface, full, full9,
and for later builds idle, rate, fullboot, eio. Raw files are named after
$FIELD_TAG (default field8).

R11 storage faults on kuberoot r11 (a copy of r2) whose state partition is a
separate, small disk served by qemu-storage-daemon over NBD.

phase full: fill the state filesystem through the API (ConfigMaps of ~900 KiB)
  to ~95 %, run config updates; fill to 100 %, run config updates for 300 s
  while forwarding client -> r11 -> gateway -> sink is pinged; try to free
  space through the API; power cut right after the last acknowledged write;
  inspect the filesystem offline; grow the disk (operator recovery), boot and
  check every acknowledged write is there.
phase eio: kill the storage daemon (every state I/O fails with EIO) for 300 s
  with updates and pings; bring it back; observe without reboot, then reboot
  and check acknowledged writes.
usage: R11-kuberoot-state.py full|eio
"""
import base64, json, os, random, signal, subprocess, sys, threading, time
from pathlib import Path

raw = Path('/work/field/raw'); d = Path('/work/vms/r11')
env = dict(os.environ, KUBECONFIG='/work/cluster-r11.conf'); nenv = dict(os.environ, KUBECONFIG='/work/trust/admin.kubeconfig')
BUILD = os.environ.get('FIELD_BUILD', '/work/field7/out'); TAG = os.environ.get('FIELD_TAG', 'field8')
phase = sys.argv[1]; out = {}; summary_file = raw / os.environ.get("R11_SUMMARY", f"R11-r11-{TAG}.summary.json")
if summary_file.exists() and os.environ.get('R11_APPEND'):
    out = json.loads(summary_file.read_text())


def say(*a):
    print(time.strftime('%H:%M:%S', time.gmtime()), *a, flush=True)


def save():
    summary_file.write_text(json.dumps(out, indent=2) + '\n')


def kubectl(a, data=None, quiet=True, node=False, timeout=25):
    cmd = ['kubectl', '--request-timeout=15s'] + (['--server', 'https://127.0.0.1:5021'] if node else []) + a
    if not quiet:
        say('$ kubectl', ' '.join(a)[:160])
    try:
        p = subprocess.run(cmd, env=nenv if node else env, input=data, capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        p = subprocess.CompletedProcess(cmd, 124, '', 'client timeout')
    if not quiet:
        print((p.stdout + p.stderr).strip()[-400:], flush=True)
    return p


def guest(vm, cmd, timeout=60):
    return subprocess.run(['/work/field/guest-command', vm, cmd], capture_output=True, text=True, timeout=timeout)


def qsd_start():
    subprocess.run(['qemu-storage-daemon', '--blockdev', f'driver=file,node-name=file0,filename={d}/state.img',
                    '--blockdev', 'driver=raw,node-name=state0,file=file0',
                    '--nbd-server', f'addr.type=unix,addr.path={d}/nbd.sock',
                    '--export', 'type=nbd,id=exp0,node-name=state0,name=state,writable=on',
                    '--daemonize', '--pidfile', f'{d}/qsd.pid'], check=True)


def qsd_kill():
    pid = int((d / 'qsd.pid').read_text()); os.kill(pid, signal.SIGKILL)
    for _ in range(50):
        if not Path(f'/proc/{pid}').exists():
            break
        time.sleep(.1)
    (d / 'qsd.pid').unlink(missing_ok=True)


def argv():
    a = json.load(open('/work/vms/r2/launch-directboot.json'))['argv']
    s = json.dumps(a)
    for x, y in [('/work/vms/r2/', '/work/vms/r11/'), ('"r2"', '"r11"'), ('sys.qcow2,format=qcow2', 'sys.raw,format=raw'),
                 ('127.0.0.1:5020-', '127.0.0.1:5021-'), ('127.0.0.1:6423-', '127.0.0.1:6424-'),
                 ('230.0.0.2:12002', '230.0.11.2:12112'), ('230.0.0.3:12003', '230.0.11.3:12113'),
                 ('52:54:00:00:02:01', '52:54:00:00:0b:01'), ('52:54:00:01:02:01', '52:54:00:01:0b:01'), ('52:54:00:02:02:01', '52:54:00:02:0b:01'),
                 ('52:54:00:30:00:12', '52:54:00:31:00:12'), ('52:54:00:30:00:11', '52:54:00:31:00:11')]:
        s = s.replace(x, y)
    s = s.replace('/work/vms/r11/directboot/vmlinuz.efi', f'{BUILD}/amd64/vmlinuz.efi').replace('/work/vms/r11/directboot/initrd.cpio', f'{BUILD}/amd64/initrd.cpio')
    s = s.replace('230.0.11.3:12113', '230.0.0.3:12003')  # the gateway probes eth2 from the transit segment
    a = json.loads(s)
    return a + ['-blockdev', f'driver=nbd,node-name=state,server.type=unix,server.path={d}/nbd.sock,export=state,reconnect-delay=5',
                '-device', 'virtio-blk-pci,drive=state,addr=0x8,werror=report,rerror=report']


def vm_start():
    if not (d / 'vars.fd').exists():
        subprocess.run(['cp', '/usr/share/OVMF/OVMF_VARS_4M.fd', str(d / 'vars.fd')], check=True)
    if not (d / 'qsd.pid').exists():
        qsd_start()
    (d / 'launch.json').write_text(json.dumps({'argv': argv()}, indent=1))
    subprocess.run(argv(), check=True)


def vm_kill():
    pid = int((d / 'pid').read_text()); os.kill(pid, signal.SIGKILL)
    for _ in range(50):
        if not Path(f'/proc/{pid}').exists():
            break
        time.sleep(.1)
    (d / 'pid').unlink(missing_ok=True)


def wait_api(limit=300):
    t = time.monotonic()
    while time.monotonic() - t < limit:
        if kubectl(['get', '--raw', '/readyz'], timeout=10).returncode == 0:
            return round(time.monotonic() - t, 1)
        time.sleep(2)


def fs_offline(tag):
    """dumpe2fs/e2fsck -n of the state partition while the VM is stopped."""
    part = d / f'state-{tag}.ext4'
    lay = json.loads((d / 'layout.json').read_text())
    subprocess.run(f'dd if={d}/state.img of={part} bs=512 skip={lay["partition_first_sector"]} conv=sparse status=none', shell=True, check=True)
    h = subprocess.run(['dumpe2fs', '-h', str(part)], capture_output=True, text=True).stdout
    f = subprocess.run(['e2fsck', '-fn', str(part)], capture_output=True, text=True)
    part.unlink()
    val = lambda k: int(next(l.split(':')[1] for l in h.splitlines() if l.startswith(k)).strip())
    r = {'block_count': val('Block count'), 'free_blocks': val('Free blocks'), 'reserved_blocks': val('Reserved block count'),
         'block_size': val('Block size'), 'state': next((l.split(':')[1].strip() for l in h.splitlines() if l.startswith('Filesystem state')), None),
         'errors_behavior': next((l.split(':')[1].strip() for l in h.splitlines() if l.startswith('Errors behavior')), None),
         'e2fsck_n_exit': f.returncode, 'e2fsck_n_tail': (f.stdout + f.stderr).strip().splitlines()[-6:]}
    r['used_pct'] = round(100 * (r['block_count'] - r['free_blocks']) / r['block_count'], 2)
    (raw / f'R11-r11-{TAG}-fs-{tag}.txt').write_text(h + '\n--- e2fsck -fn ---\n' + f.stdout + f.stderr)
    say(f'offline fs {tag}:', json.dumps({k: v for k, v in r.items() if k != 'e2fsck_n_tail'}))
    return r


def route(m):
    return json.dumps({'apiVersion': 'router.kuberoot.dev/v1alpha1', 'kind': 'Route', 'metadata': {'name': 'field-r11'},
                       'spec': {'destination': '198.51.103.0/24', 'gateway': '198.18.0.2', 'link': 'eth4', 'metric': m}})


acked = []  # every metric value the API acknowledged, in order


def updates(seconds, rate, tag):
    lat = []; errs = {}; n = 0; t0 = time.monotonic()
    while time.monotonic() - t0 < seconds:
        n += 1; m = 1000 + random.randrange(60000); t = time.monotonic()
        p = kubectl(['apply', '-f', '-'], route(m))
        lat.append(time.monotonic() - t)
        if p.returncode == 0:
            acked.append(m)
        else:
            e = (p.stderr.strip().splitlines() or ['?'])[-1][:200]; errs[e] = errs.get(e, 0) + 1
        time.sleep(max(0, t0 + n / rate - time.monotonic()))
    lat.sort()
    r = {'tag': tag, 'offered_per_s': rate, 'attempts': n, 'acked': n - sum(errs.values()), 'errors': errs,
         'p50_s': round(lat[len(lat) // 2], 3), 'max_s': round(lat[-1], 3), 'last_acked_metric': acked[-1] if acked else None}
    say(tag, json.dumps(r)[:600]); return r


def ping_start(tag, seconds):
    guest('client', f'pkill fping; rm -f /tmp/R11-{tag}-fwd.txt; nohup timeout {seconds} fping -D -l -p 200 198.19.0.1 >/tmp/R11-{tag}-fwd.txt 2>&1 </dev/null &')
    return time.time()


def ping_collect(tag, started):
    guest('client', 'pkill fping; sleep 1')
    t = guest('client', f'cat /tmp/R11-{tag}-fwd.txt').stdout
    (raw / f'R11-r11-{TAG}-{tag}-fwd.txt').write_text(t)
    ok = sum(1 for l in t.splitlines() if 'bytes' in l and l.startswith('['))
    expected = int((time.time() - started) / .2)
    r = {'replies': ok, 'expected_about': expected}
    say(tag, 'forwarding:', json.dumps(r)); return r


def services():
    p = kubectl(['get', 'nodeservices', '-o', 'json'], node=True)
    try:
        return {i['metadata']['name']: {k: i['status'].get(k) for k in ('state', 'restarts', 'lastExit')} for i in json.loads(p.stdout)['items']}
    except Exception:
        return {'error': (p.stdout + p.stderr)[-300:]}


def log_tail(svc, n=15):
    p = kubectl(['get', '--raw', f'/apis/node.kuberoot.dev/v1alpha1/nodeservices/{svc}/log'], node=True, timeout=30)
    return (p.stdout + p.stderr).strip().splitlines()[-n:]


def watch_services(seconds, tag):
    rows = []; t0 = time.monotonic()
    while time.monotonic() - t0 < seconds:
        rows.append({'t': round(time.monotonic() - t0, 1), 'services': services()}); time.sleep(10)
    (raw / f'R11-r11-{TAG}-{tag}-services.json').write_text(json.dumps(rows, indent=1) + '\n')
    return rows


def verify(tag):
    p = kubectl(['get', 'route', 'field-r11', '-o', 'json'])
    m = json.loads(p.stdout)['spec']['metric'] if p.returncode == 0 else None
    r = {'last_acked_metric': acked[-1] if acked else None, 'metric_after': m, 'acked_write_lost': bool(acked) and m != acked[-1],
         'get_error': None if p.returncode == 0 else (p.stderr.strip()[-200:])}
    say(tag, 'verify:', json.dumps(r)); return r


def scene(name, body):
    path = Path(f'/work/field/scene-{name}.sh')
    path.write_text('#!/bin/bash\nsource /work/field/lib.sh\nset +e\nexport KUBECONFIG=/work/cluster-r11.conf\n' + body)
    r = subprocess.run(['bash', '/work/field/capture.sh', name, str(path)], env=dict(os.environ, DEFER_RENDER='1'), capture_output=True, text=True)
    say('scene', name, 'exit', r.returncode)


SCENE_FULL = '''show "KUBECONFIG=/work/trust/admin.kubeconfig kubectl --server https://127.0.0.1:5021 get osconfigs -o jsonpath='{.items[0].status.stateStorage}'; echo"
show "KUBECONFIG=/work/trust/admin.kubeconfig kubectl --server https://127.0.0.1:5021 get nodeservices"
show "KUBECONFIG=/work/trust/admin.kubeconfig kubectl --server https://127.0.0.1:5021 get --raw /apis/node.kuberoot.dev/v1alpha1/nodeservices/kuberoot-router/log | tail -4 | cut -c1-200"
show "timeout 20 kubectl get routes"
show "/work/field/guest-command client 'ping -c 10 -i 0.2 -W 1 198.19.0.1 | tail -2'"
'''

SCENE_EIO = '''show "KUBECONFIG=/work/trust/admin.kubeconfig kubectl --server https://127.0.0.1:5021 get osconfigs -o jsonpath='{.items[0].status.stateStorage}'; echo"
show "KUBECONFIG=/work/trust/admin.kubeconfig kubectl --server https://127.0.0.1:5021 get nodeservices"
show "timeout 20 kubectl apply -f /work/field/R11-route-sample.json"
show "/work/field/guest-command client 'ping -c 10 -i 0.2 -W 1 198.19.0.1 | tail -2'"
'''

SCENE = '''show "kubectl apply -f /work/field/R11-route-sample.json"
show "kubectl get route field-r11 -o jsonpath='{.spec.metric}'; echo"
show "KUBECONFIG=/work/trust/admin.kubeconfig kubectl --server https://127.0.0.1:5021 get nodeservices"
show "KUBECONFIG=/work/trust/admin.kubeconfig kubectl --server https://127.0.0.1:5021 get --raw /apis/node.kuberoot.dev/v1alpha1/nodeservices/kine/log | tail -4 | cut -c1-200"
show "/work/field/guest-command client 'ping -c 10 -i 0.2 -W 1 198.19.0.1 | tail -2'"
'''


def debugfs(cmd, write=True):
    """Run debugfs on the state partition inside state.img (VM stopped)."""
    lay = json.loads((d / 'layout.json').read_text()); part = d / 'part.ext4'
    subprocess.run(f'dd if={d}/state.img of={part} bs=512 skip={lay["partition_first_sector"]} conv=sparse status=none', shell=True, check=True)
    subprocess.run(['e2fsck', '-fy', str(part)], capture_output=True)  # replay the journal first, as a mount would
    p = subprocess.run(['debugfs'] + (['-w'] if write else []) + ['-R', cmd, str(part)], capture_output=True, text=True)
    subprocess.run(f'dd if={part} of={d}/state.img bs=512 seek={lay["partition_first_sector"]} conv=notrunc status=none', shell=True, check=True)
    part.unlink(); return p


def prefill(name, leave_bytes):
    """Write a filler file into the stopped state filesystem leaving leave_bytes free (root's reserve included)."""
    fs = fs_offline(f'before-{name}'); free = fs['free_blocks'] * fs['block_size']
    size = max(0, free - leave_bytes - 4 * 1024 * 1024)  # indirect/extent blocks of the filler itself
    filler = d / 'filler.bin'
    subprocess.run(['truncate', '-s', '0', str(filler)]); subprocess.run(f"tr '\\0' x < /dev/zero | head -c {size} > {filler}", shell=True, check=True)  # not zeros: debugfs would write a sparse file
    p = debugfs(f'write {filler} /field-{name}.fill'); filler.unlink()
    r = fs_offline(f'after-{name}'); r['filler_bytes'] = size; r['debugfs'] = (p.stdout + p.stderr).strip()[-200:]
    return r


def stop_vm():
    subprocess.run(['python3', '/work/field/qmp.py', 'r11', 'system_powerdown'], capture_output=True)
    for _ in range(120):
        if not (d / 'pid').exists():
            break
        time.sleep(1)
    else:
        vm_kill()


def boot(tag):
    vm_start(); t = time.monotonic(); ready = wait_api(400)
    out[f'api_ready_{tag}_s'] = ready; time.sleep(15)
    out[f'services_{tag}'] = services()
    return ready



def write_root():
    lay = subprocess.run(['sgdisk', '-i', '2', str(d / 'sys.raw')], capture_output=True, text=True).stdout
    first = int(next(l.split(':')[1].split()[0] for l in lay.splitlines() if l.startswith('First sector')))
    name = next(l.split(':', 1)[1].strip() for l in lay.splitlines() if l.startswith('Partition name'))
    assert name == "'kuberoot-root-a'", name
    subprocess.run(f'dd if={BUILD}/amd64/rootfs.squashfs of={d}/sys.raw bs=1M seek={first * 512 // 1048576} conv=notrunc status=none', shell=True, check=True)
    say('root-a of r11 now holds', open(f'{BUILD}/amd64/VERSION').read().strip())


def fresh_state():
    lay = json.loads((d / 'layout.json').read_text()); p = lay['partition_first_sector']
    subprocess.run(f"""cd {d} && rm -f state.img && truncate -s {lay['state_disk_mib']}M state.img && sgdisk -n 1:{p}:0 -c 1:kuberoot-state state.img >/dev/null &&
      pend=$(sgdisk -i 1 state.img | awk '/Last sector/{{print $3}}') && cp --sparse=always state-src.ext4 part.ext4 &&
      truncate -s $(( (pend - {p} + 1) * 512 )) part.ext4 && resize2fs part.ext4 >/dev/null 2>&1 && e2fsck -fn part.ext4 >/dev/null &&
      dd if=part.ext4 of=state.img bs=512 seek={p} conv=notrunc,sparse status=none && rm -f part.ext4""", shell=True, check=True)


def addresses():
    p = kubectl(['get', 'osconfigs', '-o', 'jsonpath={.items[0].status.addresses}'], node=True)
    return p.stdout


if phase == 'boot':
    if (d / 'pid').exists():
        stop_vm()
    if (d / 'qsd.pid').exists():
        qsd_kill()
    write_root(); fresh_state()
    boot('field8')
    out['version'] = kubectl(['get', 'osconfigs', '-o', 'jsonpath={.items[0].status.version}'], node=True).stdout
    out['ping_ok'] = guest('client', 'ping -c 3 -W 1 198.19.0.1').returncode == 0
    say('field.8 up:', json.dumps({k: out.get(k) for k in ('api_ready_field8_s', 'version', 'ping_ok')}))
    save()

if phase == 'iface':
    before = addresses(); say('addresses before:', before)
    saved = json.loads(kubectl(['get', 'interface', 'g-lan', '-o', 'json']).stdout)
    kubectl(['delete', 'interface', 'g-lan'], quiet=False); time.sleep(10)
    after = addresses(); say('addresses after deleting Interface g-lan:', after)
    obj = {'apiVersion': saved['apiVersion'], 'kind': 'Interface', 'metadata': {'name': 'g-lan'}, 'spec': saved['spec']}
    kubectl(['apply', '-f', '-'], json.dumps(obj), quiet=False); time.sleep(10)
    out['iface'] = {'before': before, 'after_delete': after, 'removed': '192.168.10.1' in before and '192.168.10.1' not in after,
                    'after_restore': addresses()}
    say('iface:', json.dumps(out['iface'])); save()

if phase == 'full':
    stop_vm()
    out['prefill100'] = prefill('100', 0); save()
    boot('full100')
    st = ping_start('f8-100', 400)
    out['updates_full100'] = updates(120, 1, 'updates at 100 % (field.8)')
    out['fwd_full100'] = ping_collect('f8-100', st)
    out['logs_full100'] = {s: log_tail(s) for s in ('kine', 'kube-apiserver', 'kuberoot-router')}
    scene('t-R11-kuberoot-enospc-field8', SCENE); save()
    stop_vm(); p = debugfs('rm /field-100.fill')
    out['fs_after_recovery'] = fs_offline('after-recovery'); boot('recovered')
    out['verify_after_recovery'] = verify('after recovery'); save()
if phase == 'full9':
    # the state partition is still full from the field.8 run; boot the next build on it
    if (d / 'pid').exists():
        vm_kill()
    write_root()
    out['fs_full9_before_boot'] = fs_offline('full9-before-boot')
    boot('full9')
    out['version_full9'] = kubectl(['get', 'osconfigs', '-o', 'jsonpath={.items[0].status.version}'], node=True).stdout
    st = ping_start('f9-100', 400)
    out['updates_full9'] = updates(120, 1, 'updates at 100 % (field.9)')
    out['fwd_full9'] = ping_collect('f9-100', st)
    out['logs_full9'] = {s: log_tail(s) for s in ('kine', 'kube-apiserver', 'kuberoot-router')}
    scene('t-R11-kuberoot-enospc-field9', SCENE); save()
    stop_vm(); debugfs('rm /field-100.fill')
    out['fs_full9_after_recovery'] = fs_offline('full9-after-recovery'); boot('full9-recovered')
    out['verify_full9_after_recovery'] = verify('after recovery'); save()
def blockstats():
    """Bytes, operations and flushes written per disk of r11 (system disk and the NBD state disk)."""
    q = json.loads(subprocess.check_output(['python3', '/work/field/qmp.py', 'r11', 'query-blockstats']))
    return {(b.get('qdev') or b.get('device') or b.get('node-name')): {k: b['stats'][k] for k in ('wr_bytes', 'wr_operations', 'flush_operations')}
            for b in q if b.get('qdev') or b.get('device')}


def delta(a, b, seconds):
    r = {dev: {k: b[dev][k] - a[dev][k] for k in a[dev]} for dev in a if dev in b}
    total = sum(v['wr_bytes'] for v in r.values())
    return {'seconds': round(seconds, 1), 'per_disk': r, 'wr_bytes': total, 'mb_per_hour': round(total * 3600 / seconds / 1e6, 1),
            'gb_per_day': round(total * 86400 / seconds / 1e9, 2)}


def storage():
    p = kubectl(['get', 'osconfigs', '-o', 'jsonpath={.items[0].status.stateStorage}'], node=True)
    try:
        return json.loads(p.stdout) if p.returncode == 0 and p.stdout else {'missing': (p.stdout + p.stderr)[-200:]}
    except ValueError:
        return {'unparsed': p.stdout[-200:]}


def stop_others():
    """r2 has r11's transit address and OpenWrt the G addresses: both stop while r11 runs."""
    for vm in ('r2', 'openwrt'):
        f = Path(f'/work/vms/{vm}/pid')
        proc = Path('/proc/' + f.read_text().strip()) if f.exists() else None
        if proc and proc.exists():
            say(f'$ qmp {vm} system_powerdown  # same addresses as r11')
            subprocess.run(['python3', '/work/field/qmp.py', vm, 'system_powerdown'], capture_output=True)
            for _ in range(180):
                if not proc.exists():
                    break
                time.sleep(1)
            else:
                raise SystemExit(f'{vm} did not power down')
            out.setdefault('stopped', []).append(vm)


def first_reply_after(text, t0):
    for l in text.splitlines():
        if l.startswith('[') and 'bytes' in l:
            return round(float(l[1:l.index(']')]) - t0, 1)


if phase == 'start':
    stop_others()
    if (d / 'pid').exists():
        stop_vm()
    if (d / 'qsd.pid').exists():
        qsd_kill()
    write_root(); fresh_state()
    boot(TAG)
    out['version'] = kubectl(['get', 'osconfigs', '-o', 'jsonpath={.items[0].status.version}'], node=True).stdout
    out['ping_ok'] = guest('client', 'ping -c 3 -W 1 198.19.0.1').returncode == 0
    out['state_storage'] = storage()
    say(TAG, 'up:', json.dumps({k: out.get(k) for k in (f'api_ready_{TAG}_s', 'version', 'ping_ok', 'state_storage')}))
    save()

if phase == 'idle':
    seconds = int(sys.argv[2]); rows = []; t0 = time.monotonic(); b0 = blockstats(); rows.append({'t': 0, 'disks': b0})
    while time.monotonic() - t0 < seconds:
        time.sleep(min(60, max(1, seconds - (time.monotonic() - t0))))
        rows.append({'t': round(time.monotonic() - t0, 1), 'disks': blockstats()})
    (raw / f'R11-r11-{TAG}-idle.jsonl').write_text(''.join(json.dumps(r) + '\n' for r in rows))
    out['idle'] = delta(b0, rows[-1]['disks'], rows[-1]['t']); out['idle']['version'] = open(f'{BUILD}/amd64/VERSION').read().strip()
    say('idle:', json.dumps(out['idle'])); save()

if phase == 'rate':
    seconds = int(sys.argv[2]); kubectl(['apply', '-f', '-'], route(100), quiet=False); time.sleep(5)
    lat = []; errs = 0; n = 0; b0 = blockstats(); t0 = time.monotonic()
    while time.monotonic() - t0 < seconds:
        n += 1; t = time.monotonic(); p = kubectl(['apply', '-f', '-'], route(100 + n % 1000))
        lat.append(time.monotonic() - t); errs += p.returncode != 0
        time.sleep(max(0, t0 + n / 10 - time.monotonic()))
    el = time.monotonic() - t0; b1 = blockstats(); lat.sort()
    r = delta(b0, b1, el)
    r.update(offered_per_s=10, updates=n, errors=errs, achieved_per_s=round(n / el, 2), p50_s=round(lat[len(lat) // 2], 3),
             p99_s=round(lat[int(len(lat) * .99) - 1], 3), bytes_per_change=round(r['wr_bytes'] / max(1, n - errs)))
    out['rate'] = r; say('rate:', json.dumps(r)); save()

if phase == 'fullboot':
    # The state filesystem is filled offline to the last block, with the space
    # reserve (variant keep) or without it (variant none: as if it were spent).
    variant = sys.argv[2]; key = f'fullboot_{variant}'; res = out[key] = {}
    stop_vm()
    if variant == 'none':
        res['reserve_removed'] = (debugfs('rm /lib/kuberoot/space-reserve').stderr or '').strip()[-200:]
    res['prefill'] = prefill(f'100-{variant}', 0); save()
    guest('client', 'pkill fping; true')
    vm_start(); t0 = time.time()
    guest('client', f'rm -f /tmp/R11-boot-{variant}.txt; nohup timeout 330 fping -D -l -p 200 198.19.0.1 >/tmp/R11-boot-{variant}.txt 2>&1 </dev/null &')
    res['api_ready_s'] = wait_api(240); time.sleep(10)
    res['services'] = services(); res['state_storage'] = storage()
    res['updates'] = updates(60, 1, f'updates on a full state ({variant})')
    left = 330 - (time.time() - t0)
    if left > 0:
        time.sleep(left)
    guest('client', 'pkill fping; sleep 1')
    t = guest('client', f'cat /tmp/R11-boot-{variant}.txt').stdout; (raw / f'R11-r11-{TAG}-boot-{variant}-fwd.txt').write_text(t)
    res['fwd'] = {'replies': sum(1 for l in t.splitlines() if 'bytes' in l and l.startswith('[')), 'expected_about': 330 * 5,
                  'first_reply_after_start_s': first_reply_after(t, t0)}
    res['logs'] = {s: log_tail(s) for s in ('kine', 'kube-apiserver', 'kuberoot-router')}
    res['routes'] = kubectl(['get', 'routes', '-o', 'name']).stdout.split()
    say(key, json.dumps({k: v for k, v in res.items() if k not in ('logs', 'prefill')})[:1500])
    scene(f't-R11-kuberoot-full-{variant}-{TAG}', SCENE_FULL); save()
    stop_vm(); debugfs(f'rm /field-100-{variant}.fill')
    res['fs_after_recovery'] = fs_offline(f'after-recovery-{variant}'); boot(f'recovered_{variant}')
    res['verify_after_recovery'] = verify('after recovery'); res['state_storage_after_recovery'] = storage(); save()

if phase == 'eio':
    res = out['eio'] = {}
    res['updates_before'] = updates(30, 1, 'updates before the fault'); res['storage_before'] = storage()
    st = ping_start('eio', 600)
    say('$ kill -9 qemu-storage-daemon  # every state I/O now fails with EIO (werror/rerror=report)')
    qsd_kill(); t_fault = time.time(); seen = []

    def watch():
        t = time.monotonic()
        while time.monotonic() - t < 300:
            seen.append({'t': round(time.time() - t_fault, 1), 'services': services(), 'state_storage': storage()}); time.sleep(10)
    th = threading.Thread(target=watch); th.start()
    res['updates_eio'] = updates(300, 1, 'updates during EIO'); th.join()
    (raw / f'R11-r11-{TAG}-eio-watch.json').write_text(json.dumps(seen, indent=1) + '\n')
    flagged = [r['t'] for r in seen if r['state_storage'].get('state') not in (None, 'Healthy')]
    res['storage_flagged_after_s'] = flagged[0] if flagged else None; res['storage_during'] = seen[-1]['state_storage'] if seen else None
    res['logs_eio'] = {s: log_tail(s) for s in ('kine', 'kube-apiserver', 'kuberoot-router')}
    scene(f't-R11-kuberoot-eio-{TAG}', SCENE_EIO)
    say('$ qemu-storage-daemon again  # storage back'); qsd_start(); res['fault_seconds'] = round(time.time() - t_fault, 1)
    time.sleep(20); res['updates_after_restore'] = updates(60, 1, 'updates after storage returned (no reboot)')
    res['storage_after_restore'] = storage(); res['fwd_eio'] = ping_collect('eio', st); save()
    say('$ qmp r11 system_reset'); subprocess.run(['python3', '/work/field/qmp.py', 'r11', 'system_reset'], check=True, capture_output=True)
    time.sleep(5); res['api_ready_after_reboot_s'] = wait_api(); time.sleep(20)
    res['verify_after_reboot'] = verify('after reboot'); res['storage_after_reboot'] = storage()
    res['updates_after_reboot'] = updates(20, 1, 'updates after reboot')
    res['ping_ok_after_reboot'] = guest('client', 'ping -c 3 -W 1 198.19.0.1').returncode == 0
    save()

say('done', phase)
