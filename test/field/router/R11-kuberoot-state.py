#!/usr/bin/env python3
"""R11 storage faults on kuberoot r11 (a copy of r2) whose state partition is a
separate, small disk served by qemu-storage-daemon over NBD.

Filling the disk is R11-kuberoot-prefill.py: filling it through the API with
ConfigMaps runs the API server out of memory before the disk is full.
phase eio: kill the storage daemon (every state I/O fails with EIO) for 300 s
  with updates and pings; bring it back; observe without reboot, then reboot
  and check acknowledged writes.
usage: R11-kuberoot-state.py eio
"""
import base64, json, os, random, signal, subprocess, sys, threading, time
from pathlib import Path

raw = Path('/work/field/raw'); d = Path('/work/vms/r11')
env = dict(os.environ, KUBECONFIG='/work/cluster-r11.conf'); nenv = dict(os.environ, KUBECONFIG='/work/trust/admin.kubeconfig')
phase = sys.argv[1]; out = {}; summary_file = raw / f'R11-r11-{phase}.summary.json'


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
    a = json.loads(s.replace('/work/vms/r11/directboot/', '/work/vms/r2/directboot/'))  # r2's field.6 kernel, read only
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
    (raw / f'R11-r11-{phase}-fs-{tag}.txt').write_text(h + '\n--- e2fsck -fn ---\n' + f.stdout + f.stderr)
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
    (raw / f'R11-r11-{tag}-fwd.txt').write_text(t)
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
    (raw / f'R11-r11-{tag}-services.json').write_text(json.dumps(rows, indent=1) + '\n')
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


SCENE = '''show "kubectl apply -f /work/field/R11-route-sample.json"
show "kubectl get route field-r11 -o jsonpath='{.spec.metric}'; echo"
show "KUBECONFIG=/work/trust/admin.kubeconfig kubectl --server https://127.0.0.1:5021 get nodeservices"
show "KUBECONFIG=/work/trust/admin.kubeconfig kubectl --server https://127.0.0.1:5021 get --raw /apis/node.kuberoot.dev/v1alpha1/nodeservices/kine/log | tail -4 | cut -c1-200"
show "/work/field/guest-command client 'ping -c 10 -i 0.2 -W 1 198.19.0.1 | tail -2'"
'''

if not (d / 'pid').exists() or not Path('/proc/' + (d / 'pid').read_text().strip()).exists():
    out['fs_before_boot'] = fs_offline('before-boot')
    vm_start()
out['api_ready_s'] = wait_api(); time.sleep(10)
kubectl(['apply', '-f', '-'], route(100), quiet=False)
out['ping_ok_at_start'] = guest('client', 'ping -c 3 -W 1 198.19.0.1').returncode == 0
Path('/work/field/R11-route-sample.json').write_text(route(4242))
save()

if phase == 'eio':
    out['updates_before'] = updates(30, 1, 'updates before the fault')
    st = ping_start('eio', 600)
    say('$ kill -9 qemu-storage-daemon  # every state I/O now fails with EIO (werror/rerror=report)')
    qsd_kill(); t_fault = time.time()
    th = threading.Thread(target=lambda: out.__setitem__('services_eio', watch_services(300, 'eio'))); th.start()
    out['updates_eio'] = updates(300, 1, 'updates during EIO'); th.join()
    out['logs_eio'] = {s: log_tail(s) for s in ('kine', 'kube-apiserver', 'kuberoot-router')}
    scene('t-R11-kuberoot-eio', SCENE)
    say('$ qemu-storage-daemon again  # storage back'); qsd_start(); out['fault_seconds'] = round(time.time() - t_fault, 1)
    time.sleep(20); out['updates_after_restore'] = updates(60, 1, 'updates after storage returned (no reboot)')
    out['services_after_restore'] = services(); out['fwd_eio'] = ping_collect('eio', st); save()
    say('$ qmp r11 system_reset'); subprocess.run(['python3', '/work/field/qmp.py', 'r11', 'system_reset'], check=True, capture_output=True)
    time.sleep(5); out['api_ready_after_reboot_s'] = wait_api(); time.sleep(20)
    out['verify_after_reboot'] = verify('after reboot'); out['services_after_reboot'] = services()
    out['updates_after_reboot'] = updates(20, 1, 'updates after reboot')
    out['ping_ok_after_reboot'] = guest('client', 'ping -c 3 -W 1 198.19.0.1').returncode == 0
    save()
say('done', phase)
