#!/usr/bin/env python3
"""Observe absence of automatic rollback, then explicitly repair via observer NIC."""
import json,subprocess,time
from pathlib import Path
raw=Path('/work/field/raw')
def observer(cmd):return subprocess.run(['/work/field/guest-command','debian',cmd],capture_output=True,text=True,timeout=15)
def production(cmd):return subprocess.run(['ssh','-F','/work/field/private/production-ssh.conf','debian-production',cmd],capture_output=True,text=True,timeout=8)
while not (raw/'R7-debian-small.exit').exists():time.sleep(3)
phases=[('address','ip addr del 198.18.0.1/24 dev enp0s2; ip addr add 198.18.0.99/24 dev enp0s2','ip addr del 198.18.0.99/24 dev enp0s2; ip addr replace 198.18.0.1/24 dev enp0s2; ip route replace default via 198.18.0.2 dev enp0s2'),('return-route','ip route del default','ip route replace default via 198.18.0.2'),('vlan','ip link add link enp0s2 name badmanagement type vlan id 999; ip link set badmanagement up; ip addr del 198.18.0.1/24 dev enp0s2; ip addr add 198.18.0.1/24 dev badmanagement','ip link del badmanagement; ip addr replace 198.18.0.1/24 dev enp0s2; ip route replace default via 198.18.0.2'),('firewall',"nft add table inet fieldlock; nft 'add chain inet fieldlock input { type filter hook input priority -100; policy accept; }'; nft add rule inet fieldlock input iifname enp0s2 tcp dport 22 drop",'nft delete table inet fieldlock')]
for phase,inject,repair in phases:
 p=production('date -u; echo "$SSH_CONNECTION"')
 if p.returncode:raise SystemExit('positive production management control failed')
 print(time.strftime('%FT%TZ',time.gmtime()),phase,'production positive control',p.stdout,flush=True)
 # Schedule via authenticated production transport; do not block on severed SSH.
 cmd='nohup sh -c '+__import__('shlex').quote('sleep 1; '+inject)+' >/tmp/R1-fault.log 2>&1 </dev/null &'
 print('$ ssh debian-production',cmd,flush=True);p=production(cmd);start=time.monotonic();rows=[]
 time.sleep(2)
 while time.monotonic()-start<95:
  t=time.monotonic()
  try:p=production('true');result={'exit':p.returncode,'stderr':p.stderr}
  except subprocess.TimeoutExpired:result={'timeout':True}
  result.update(at=time.time(),elapsed_s=time.monotonic()-start,probe_s=time.monotonic()-t);rows.append(result);print(json.dumps(result),flush=True);time.sleep(2)
 print('Explicit observer repair AFTER measurement:',repair,flush=True);p=observer(repair);print(p.stdout+p.stderr,flush=True);time.sleep(3)
 p=production('date -u; echo "$SSH_CONNECTION"')
 summary={'phase':phase,'native_trial_mechanism':'none','automatic_management_recovery_after_first_loss':any(r.get('exit')==0 for r in rows[1:]),'manual_observer_repair':True,'after_repair_exit':p.returncode,'probes':rows}
 (raw/f'R1-debian-{phase}.json').write_text(json.dumps(summary,indent=2)+'\n');print(json.dumps(summary),flush=True)
(raw/'R1-debian.exit').write_text('0\n')
