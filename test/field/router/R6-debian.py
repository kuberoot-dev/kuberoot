#!/usr/bin/env python3
import json,subprocess,time,base64,sys
from pathlib import Path
raw=Path('/work/field/raw')
def guest(vm,cmd,timeout=90):
 print(time.strftime('%FT%TZ',time.gmtime()),'$ guest-command',vm,cmd if len(cmd)<1000 else cmd[:80]+' [generated file transfer, full source in script]',flush=True)
 p=subprocess.run(['/work/field/guest-command',vm,cmd],capture_output=True,text=True,timeout=timeout);print(p.stdout+p.stderr,flush=True);return p
while not (raw/'R4-debian-reuse-reboot.summary.json').exists():time.sleep(2)
guest('debian','if ! test -f /root/field-nft-before-R6.conf; then cp /etc/nftables.conf /root/field-nft-before-R6.conf; nft list ruleset >/root/field-before-R6.nft; fi')
for vm in ['client','sink']:
 code=base64.b64encode(Path('/work/field/udp-probes.py').read_bytes()).decode();guest(vm,f'echo {code} | base64 -d >/tmp/udp-probes.py')
guest('sink','nohup python3 /tmp/udp-probes.py server --seconds 1200 >/tmp/R6-server.log 2>&1 </dev/null &')
rows=json.loads((raw/'R6-debian-scale.json').read_text()) if (raw/'R6-debian-scale.json').exists() else []
for count in ([int(x) for x in sys.argv[1:]] or [100,1000,10000,20000]):
 rules=['flush ruleset','table inet fieldscale {','chain forward { type filter hook forward priority 0; policy drop;','ip saddr 198.19.0.1 udp sport 9000 accept']
 rules += [f'ip saddr 203.{(i>>16)&255}.{(i>>8)&255}.{i&255} udp dport 9000 drop comment "rule-{i}"' for i in range(count)]
 rules += ['iifname "enp0s3" oifname "enp0s2" udp dport 9000 accept','}','}']
 text='\n'.join(rules)+'\n';encoded=base64.b64encode(text.encode()).decode()
 # First install deny policy before starting forbidden probes.
 print('$ guest-command debian cat > /tmp/fieldscale.nft (generated rules supplied on stdin)',flush=True)
 subprocess.run(['/work/field/guest-command','debian','cat >/tmp/fieldscale.nft'],input=text,text=True,check=True)
 prepared=guest('debian','nft -c -f /tmp/fieldscale.nft && nft -f /tmp/fieldscale.nft')
 if prepared.returncode:raise SystemExit('ruleset validation/application failed; no measurement')
 guest('client',f'nohup python3 /tmp/udp-probes.py client --seconds 65 --output /tmp/R6-{count}-probes.jsonl >/tmp/R6-{count}-probe.log 2>&1 </dev/null &')
 time.sleep(2)
 for phase,cmd in [('full-replace','nft -f /tmp/fieldscale.nft'),('one-rule-edit','nft -a list chain inet fieldscale forward | sed -n \'/comment "rule-0"/s/.*# handle //p\' | xargs -I HANDLE nft replace rule inet fieldscale forward handle HANDLE ip saddr 203.0.0.0 udp dport 9000 drop comment edited'),('description-only','nft -a list chain inet fieldscale forward | sed -n \'/comment "edited"/s/.*# handle //p\' | xargs -I HANDLE nft replace rule inet fieldscale forward handle HANDLE ip saddr 203.0.0.0 udp dport 9000 drop comment description')]:
  t=time.monotonic();p=guest('debian',cmd);rows.append({'count':count,'phase':phase,'observer_wall_s':time.monotonic()-t,'exit':p.returncode})
 p=guest('debian',"nft list ruleset >/tmp/r6-before; printf 'flush ruleset\\ntable inet bad { chain f { invalid rule; } }\\n' | nft -f -; code=$?; nft list ruleset >/tmp/r6-after; cmp /tmp/r6-before /tmp/r6-after; printf 'invalid_exit=%s preserved_exit=%s\\n' $code $?")
 rows.append({'count':count,'phase':'invalid','evidence':p.stdout+p.stderr})
 old_boot=guest('debian','cat /proc/sys/kernel/random/boot_id').stdout.strip()
 guest('debian','nft list ruleset >/etc/nftables.conf; systemctl enable nftables; systemctl reboot')
 t=time.monotonic()
 while time.monotonic()-t<100:
  p=guest('debian','cat /proc/sys/kernel/random/boot_id; nft list ruleset >/tmp/R6-reboot-rules; wc -l /tmp/R6-reboot-rules')
  if p.returncode==0 and p.stdout.splitlines()[0]!=old_boot:break
  time.sleep(2)
 rows.append({'count':count,'phase':'reboot','ssh_s':time.monotonic()-t,'exit':p.returncode,'rules_lines':p.stdout})
 time.sleep(max(0,67-(time.monotonic()-t)))
 data=subprocess.check_output(['/work/field/guest-command','client',f'cat /tmp/R6-{count}-probes.jsonl']);(raw/f'R6-debian-{count}-probes.jsonl').write_bytes(data)
 (raw/'R6-debian-scale.json').write_text(json.dumps(rows,indent=2)+'\n')
guest('debian','cp /root/field-nft-before-R6.conf /etc/nftables.conf; nft flush ruleset; nft -f /root/field-before-R6.nft')
(raw/'R6-debian.exit').write_text('0\n')
