#!/bin/bash
set -euo pipefail
mkdir -p /work/field/private /work/vms/debian
chmod 700 /work/field/private
[[ -f /work/field/private/id_ed25519 ]] || ssh-keygen -q -t ed25519 -N '' -f /work/field/private/id_ed25519
python3 - <<'PY'
from pathlib import Path
import json
key=Path('/work/field/private/id_ed25519.pub').read_text().strip()
config={'hostname':'field-debian','disable_root':False,'ssh_pwauth':False,
 'users':[{'name':'root','ssh_authorized_keys':[key]}],
 'packages':['frr','nftables','dnsmasq','iperf3','tcpdump'],
 'write_files':[{'path':'/etc/sysctl.d/90-field.conf','content':'net.ipv4.ip_forward=1\n'}],
 'runcmd':['sysctl --system','dpkg-query -W > /root/field-package-versions.txt']}
Path('/work/field/private/user-data').write_text('#cloud-config\n'+json.dumps(config))
Path('/work/field/private/meta-data').write_text('instance-id: field-router-debian-01\nlocal-hostname: field-debian\n')
PY
cloud-localds /work/vms/debian/seed.img /work/field/private/user-data /work/field/private/meta-data
