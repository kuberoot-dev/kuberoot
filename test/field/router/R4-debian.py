#!/usr/bin/env python3
import argparse,json,subprocess,time
from pathlib import Path
parser=argparse.ArgumentParser();parser.add_argument('--mixed',action='store_true');args=parser.parse_args()
base=Path('/work/field/raw')
def guest(vm,command,timeout=90):
 print(time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime()),'$ guest-command',vm,command,flush=True)
 p=subprocess.run(['/work/field/guest-command',vm,command],capture_output=True,text=True,timeout=timeout)
 print(p.stdout+p.stderr,flush=True);return p
# Per-phase reset is preparation, never hidden as automatic recovery.
guest('debian','cp /etc/dnsmasq.d/field.conf /tmp/field-dnsmasq-before-R4.conf; nft list ruleset > /tmp/field-nft-before-R4.nft; cp /var/lib/misc/dnsmasq.leases /tmp/field-leases-before-R4')
if args.mixed:
 guest('debian',"printf 'host-record=field-sink.lab,198.19.0.1\\n' > /etc/dnsmasq.d/field-mixed.conf")
 guest('client','nohup python3 /tmp/dns-load.py >/tmp/R4-dns-process.log 2>&1 </dev/null &')
 guest('client','nohup iperf3 -c 198.19.0.1 -u -b 1M -l 512 -t 300 -J >/tmp/R4-nat-load.json 2>&1 </dev/null &')
phases=[('cold100','--scenario avalanche -R 100 -p 30')]+[(f'rate-{r}',f'-r {r} -R 120 -n {r*30} -p 30') for r in (10,100,1000)]
for phase,options in phases:
 if args.mixed:phase='mixed-'+phase
 guest('debian','systemctl stop dnsmasq; truncate -s 0 /var/lib/misc/dnsmasq.leases; systemctl start dnsmasq')
 guest('client',f'tcpdump -U -n -i test0 -s 0 -w /tmp/R4-{phase}.pcap "udp port 67 or udp port 68" >/tmp/R4-{phase}-tcpdump.log 2>&1 & echo $! >/tmp/R4-capture.pid')
 time.sleep(1)
 command=f'timeout --signal=INT --kill-after=3s 35s perfdhcp -4 -B -l test0 -b mac=02:42:00:00:00:00 -s 42 {options} -x aelt > /tmp/R4-{phase}.log 2>&1; code=$?; tail -22 /tmp/R4-{phase}.log; exit $code'
 start=time.monotonic();p=guest('client',command,timeout=60)
 guest('client','kill -INT $(cat /tmp/R4-capture.pid); sleep 1')
 leases=guest('debian','cat /var/lib/misc/dnsmasq.leases').stdout
 (base/f'R4-debian-{phase}-leases.txt').write_text(leases)
 # Transfer actual raw output/pcap over the isolated observer SSH path.
 for ext in ['log','pcap']:
  q=subprocess.run(['/work/field/guest-command','client',f'cat /tmp/R4-{phase}.{ext}'],capture_output=True,check=True);(base/f'R4-debian-{phase}.{ext}').write_bytes(q.stdout)
 q=guest('client',f'python3 /tmp/dhcp-pcap-summary.py /tmp/R4-{phase}.pcap --output /tmp/R4-{phase}.summary.json')
 raw=subprocess.check_output(['/work/field/guest-command','client',f'cat /tmp/R4-{phase}.summary.json']);summary=json.loads(raw);summary.update(product='debian',phase=phase,perfdhcp_exit=p.returncode,wall_elapsed_s=time.monotonic()-start,offered_clients=100 if phase.endswith('cold100') else 120)
 (base/f'R4-debian-{phase}.summary.json').write_text(json.dumps(summary,indent=2)+'\n')
 print(json.dumps({k:v for k,v in summary.items() if k!='leases'}),flush=True)
