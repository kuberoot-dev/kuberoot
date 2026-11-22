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
# kubectl defaults to no overall request deadline. Give slow workload operations
# a bounded 10-minute budget; initial availability polling stays at 120s.
workload = [arg.replace('--request-timeout=120s', '--request-timeout=600s') for arg in cluster]
report = dict(profile=profile, timescale=1 if profile=='baseline' else 60,
              cpu_limit_millicores=100, vcpus=1, ram_mib=1024, success=False)
deadline = boot + (900 if profile == 'baseline' else 7200)

def run(args, input=None):
    timeout = 650 if '--request-timeout=600s' in args else 150
    p = subprocess.run(args, input=input, capture_output=True, text=True, timeout=timeout)
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
    report['service_restarts_at_ready'] = poll(services, deadline)
    started = time.time()
    manifest = dict(apiVersion='v1', kind='Pod', metadata=dict(name='emulator-smoke'),
                    spec=dict(restartPolicy='Never', containers=[dict(name='smoke',
                              image='busybox:1.37.0', command=['sleep','7200'])]))
    def create_pod():
        try:
            run(workload+['create','-f','-'], input=json.dumps(manifest))
        except RuntimeError as error:
            # A timed-out create may already have committed. Retrying is safe
            # for this uniquely named pod in a fresh single-guest cluster.
            if 'AlreadyExists' not in str(error): raise
        return True
    poll(create_pod, deadline)
    poll(lambda:get(['get','pod','emulator-smoke','-o','json'])['status'].get('phase')=='Running',deadline)
    report['pod_running_s'] = round(time.time()-started,1)
    report['pod_running_since_boot_s'] = round(time.time()-boot,1)
    print('POD_RUNNING',report['pod_running_since_boot_s'],flush=True)
    output=poll(lambda:run(workload+['exec','emulator-smoke','--','echo','EMULATOR_OK']),deadline)
    if output.strip()!='EMULATOR_OK': raise RuntimeError(output)
    # Standard kubectl operation and DNS, without changes to user workloads.
    report['pod_exec'] = output.strip()
    def calibrate():
        result = subprocess.run(workload+['exec','-i','emulator-smoke','--','sh','-c',
            'cat > /tmp/mips; chmod +x /tmp/mips; /tmp/mips'],
            input=(work/'mips').read_bytes(), capture_output=True, timeout=650)
        if result.returncode: raise RuntimeError(result.stderr.decode()[-1000:])
        return json.loads(result.stdout)
    report['calibration'] = poll(calibrate,deadline)
    def dns_ready():
        items=get(['get','pods','-n','kube-system','-l','k8s-app=kube-dns','-o','json'])['items']
        return bool(items) and all(any(c['type']=='Ready' and c['status']=='True'
                   for c in p['status'].get('conditions',[])) for p in items)
    poll(dns_ready,deadline)
    report['dns'] = poll(lambda:run(workload+['exec','emulator-smoke','--','nslookup','kubernetes.default.svc.cluster.local']),deadline)
    report['pods'] = [{'name':p['metadata']['name'],'phase':p['status'].get('phase'),
                       'restarts':sum(c.get('restartCount',0) for c in p['status'].get('containerStatuses',[]))}
                      for p in poll(lambda:get(['get','pods','-A','-o','json']),deadline)['items']]
    report['service_restarts_final'] = poll(services,deadline)
    report['success'] = poll(ready,deadline)==name and report['service_restarts_final']==report['service_restarts_at_ready']
except Exception as e:
    report['error'] = str(e)
    try: report['service_restarts_final']=services()
    except Exception: pass
finally:
    report['elapsed_s']=round(time.time()-boot,1)
    (work/f'{profile}-report.json').write_text(json.dumps(report,indent=2)+'\n')
    print(json.dumps(report,indent=2),flush=True)
