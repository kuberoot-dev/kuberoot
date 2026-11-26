#!/usr/bin/env python3
"""Native nftables scaling, atomic replace, edit, invalid batch and persistence."""
import json,subprocess,time
from pathlib import Path
out=Path('/tmp/R6-linux-scale.jsonl')
def apply(text,phase,count):
 start=time.monotonic();p=subprocess.run(['nft','-f','-'],input=text,text=True,capture_output=True)
 row={'phase':phase,'rules':count,'elapsed_s':time.monotonic()-start,'exit':p.returncode,'stderr':p.stderr,'wall_time':time.time()}
 with out.open('a') as f:f.write(json.dumps(row)+'\n')
 print(json.dumps(row),flush=True);return p
for count in [100,1000,10000,20000]:
 rules=['flush ruleset','table inet fieldscale {','chain forward { type filter hook forward priority 0; policy drop;','ct state established,related accept']
 for i in range(count):rules.append(f'ip saddr 203.{(i>>16)&255}.{(i>>8)&255}.{i&255} udp dport 9000 drop comment "rule-{i}"')
 rules+=['iifname "enp0s3" oifname "enp0s2" udp dport 9000 accept','}','}']
 text='\n'.join(rules)+'\n';Path('/tmp/fieldscale.nft').write_text(text)
 apply(text,'full-replace',count)
 listing=json.loads(subprocess.check_output(['nft','-j','-a','list','chain','inet','fieldscale','forward']))
 handles=[x['rule']['handle'] for x in listing['nftables'] if 'rule' in x and x['rule'].get('comment')=='rule-0']
 if len(handles)!=1:raise SystemExit('cannot identify exact rule handle')
 apply(f'replace rule inet fieldscale forward handle {handles[0]} ip saddr 203.0.0.0 udp dport 9000 drop comment "edited"','one-rule-edit',count)
 apply(f'replace rule inet fieldscale forward handle {handles[0]} ip saddr 203.0.0.0 udp dport 9000 drop comment "description-only"','description-only',count)
 before=subprocess.check_output(['nft','list','ruleset'])
 bad=apply('flush ruleset\ntable inet invalid { chain forward { this is invalid; } }','invalid-batch',count)
 after=subprocess.check_output(['nft','list','ruleset'])
 with out.open('a') as f:f.write(json.dumps({'phase':'invalid-preserves-ruleset','rules':count,'preserved':before==after,'invalid_rejected':bad.returncode!=0})+'\n')
 time.sleep(2)
