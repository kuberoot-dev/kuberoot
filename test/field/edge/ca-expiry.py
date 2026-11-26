#!/usr/bin/env python3
"""Read only the public server CA from the active kubeconfig; never print keys."""
import base64,datetime,hashlib,json,subprocess
config=json.loads(subprocess.check_output(['kubectl','config','view','--raw','--minify','-o','json']))
cluster=config['clusters'][0]['cluster'];assert not cluster.get('insecure-skip-tls-verify')
cert=base64.b64decode(cluster['certificate-authority-data'])
not_after=subprocess.check_output(['openssl','x509','-noout','-enddate'],input=cert).decode().strip().split('=',1)[1]
expiry=datetime.datetime.strptime(not_after,'%b %d %H:%M:%S %Y %Z').replace(tzinfo=datetime.timezone.utc)
print(json.dumps({'ca_pem_sha256':hashlib.sha256(cert).hexdigest(),'not_after_utc':expiry.isoformat(),'beyond_ca_rtc':(expiry+datetime.timedelta(days=1)).isoformat()}))
