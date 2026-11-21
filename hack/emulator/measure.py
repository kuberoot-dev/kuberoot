#!/usr/bin/env python3
"""Run in builder: measure.py QEMU_POD_IP baseline|emulator BOOT_EPOCH.

Same quota and artifacts for both boots. Uses wall-clock observations, since
slow TCG guests may have delayed timer interrupts. Keeps all private keys in
/work/emulator/trust; published reports contain no kubeconfigs.
"""
import json
from pathlib import Path
import subprocess
import sys
import time

work = Path('/work/emulator')
host, profile, boot = sys.argv[1], sys.argv[2], float(sys.argv[3])
node = ['/work/bin/kubectl', '--kubeconfig', str(work/'trust/admin.kubeconfig'),
        '--server', f'https://{host}:50000', '--tls-server-name=kuberoot-node', '--request-timeout=120s']
cluster = ['/work/bin/kubectl', '--kubeconfig', str(work/f'{profile}.conf'),
           '--server', f'https://{host}:6443', '--tls-server-name=10.0.2.15', '--request-timeout=120s', '-n', 'default']
report = dict(profile=profile, timescale=1 if profile=='baseline' else 60,
              cpu_limit_millicores=100, vcpus=1, ram_mib=1024, success=False)
deadline = boot + (900 if profile == 'baseline' else 2400)

def run(args):
    p = subprocess.run(args, capture_output=True, text=True, timeout=150)
    if p.returncode: raise RuntimeError(p.stderr[-1500:])
    return p.stdout

def get(args, client=cluster): return json.loads(run(client+args))

def poll(action, end):
    last = ''
    while time.time() < end:
        try:
            value = action()
            if value: return value
        except (RuntimeError, subprocess.TimeoutExpired) as e:
            last = str(e)
            print(round(time.time()-boot), last[-250:], flush=True)
        time.sleep(5)
    raise RuntimeError('deadline exceeded: '+last)

def ready():
    for n in get(['get','nodes','-o','json'])['items']:
        if any(c['type']=='Ready' and c['status']=='True' for c in n['status'].get('conditions', [])):
            return n['metadata']['name']

def services():
    return {s['metadata']['name']: s['status'].get('restarts',0)
            for s in get(['get','nodeservices','-o','json'],node)['items']}

try:
    credential = poll(lambda:get(['get','kubeconfigs','admin','-o','json'],node), deadline)
    path = work/f'{profile}.conf'
    path.write_text(credential['kubeconfig']); path.chmod(0o600)
    report['node_api_s'] = round(time.time()-boot,1)
    print('NODE_API',report['node_api_s'],flush=True)
    name = poll(ready,deadline)
    report['ready_observed_s'] = round(time.time()-boot,1)
    print('READY',report['ready_observed_s'],flush=True)
    report['service_restarts_at_ready'] = services()
    started = time.time()
    run(cluster+['run','emulator-smoke','--image=busybox:1.37.0','--restart=Never','--','sleep','3600'])
    poll(lambda:get(['get','pod','emulator-smoke','-o','json'])['status'].get('phase')=='Running',deadline)
    report['pod_running_s'] = round(time.time()-started,1)
    report['pod_running_since_boot_s'] = round(time.time()-boot,1)
    output=run(cluster+['exec','emulator-smoke','--','echo','EMULATOR_OK'])
    if output.strip()!='EMULATOR_OK': raise RuntimeError(output)
    # Standard kubectl operation and DNS, without changes to user workloads.
    report['pod_exec'] = output.strip()
    calibration = subprocess.run(cluster+['exec','-i','emulator-smoke','--','sh','-c',
        'cat > /tmp/mips; chmod +x /tmp/mips; /tmp/mips'],
        input=(work/'mips').read_bytes(), capture_output=True, timeout=150)
    if calibration.returncode == 0:
        report['calibration'] = json.loads(calibration.stdout)
    else:
        report['calibration_error'] = calibration.stderr.decode()[-1000:]
    def dns_ready():
        items=get(['get','pods','-n','kube-system','-l','k8s-app=kube-dns','-o','json'])['items']
        return bool(items) and all(any(c['type']=='Ready' and c['status']=='True'
                   for c in p['status'].get('conditions',[])) for p in items)
    poll(dns_ready,deadline)
    report['dns'] = run(cluster+['exec','emulator-smoke','--','nslookup','kubernetes.default.svc.cluster.local'])
    report['pods'] = [{'name':p['metadata']['name'],'phase':p['status'].get('phase'),
                       'restarts':sum(c.get('restartCount',0) for c in p['status'].get('containerStatuses',[]))}
                      for p in get(['get','pods','-A','-o','json'])['items']]
    report['service_restarts_final'] = services()
    report['success'] = ready()==name and report['service_restarts_final']==report['service_restarts_at_ready']
except Exception as e:
    report['error'] = str(e)
    try: report['service_restarts_final']=services()
    except Exception: pass
finally:
    report['elapsed_s']=round(time.time()-boot,1)
    (work/f'{profile}-report.json').write_text(json.dumps(report,indent=2)+'\n')
    print(json.dumps(report,indent=2),flush=True)
