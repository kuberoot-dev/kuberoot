#!/usr/bin/env python3
"""Package a real, explicitly test-only Talos extension for bad-upgrade trials.
Input executable is built in the pod; nft and its shared libraries come from
this Debian fixture. No kernel modules or production signing keys are changed.
"""
import argparse,json,pathlib,re,shutil,subprocess
p=argparse.ArgumentParser();p.add_argument('agent',type=pathlib.Path);p.add_argument('mode',choices=['init-crash','pod-network','wrong-content','incompatible-state']);p.add_argument('output',type=pathlib.Path);a=p.parse_args()
assert str(a.output).startswith('/work/field-edge/') and pathlib.Path('/dev/kvm').exists()
s=pathlib.Path(__file__).resolve().parent;root=a.output/'extension';root.mkdir(parents=True)
manifest='''version: v1alpha1
metadata:
  name: field-fault
  version: "0.1"
  author: kuberoot field laboratory
  description: Intentionally broken service, NEVER for production
  compatibility:
    talos:
      version: ">= v1.14.7 < v1.15.0"
'''
(root/'manifest.yaml').write_text(manifest)
container=root/'rootfs/usr/local/lib/containers/field-fault';container.mkdir(parents=True)
shutil.copy2(a.agent,container/'field-fault-agent')
if a.mode in ['pod-network','wrong-content']:
 nft=pathlib.Path(shutil.which('nft'));files=[nft]
 output=subprocess.check_output(['ldd',str(nft)],text=True)
 files += [pathlib.Path(x) for x in re.findall(r'(/[^\s()]+)',output)]
 # ldd includes transitive dependencies on this platform; record exact hashes later.
 for source in dict.fromkeys(files):
  target=container/str(source).lstrip('/');target.parent.mkdir(parents=True,exist_ok=True);shutil.copy2(source.resolve(),target)
config=root/'rootfs/usr/local/etc/containers';config.mkdir(parents=True)
service=f'''name: field-fault
container:
  entrypoint: ./field-fault-agent
  args: [{a.mode}]
  environment:
    - PATH=/usr/sbin:/usr/bin:/sbin:/bin
  mounts:
    - source: /var/lib/field-records
      destination: /var/lib/field-records
      type: bind
      options: [bind, rw]
    - source: /sys/class/dmi/id/product_serial
      destination: /sys/class/dmi/id/product_serial
      type: bind
      options: [bind, ro]
depends:
  - service: cri
restart: always
logToConsole: true
'''
(config/'field-fault.yaml').write_text(service)
subprocess.run(['python3',str(s/'oci-image.py'),'pack',str(root),str(a.output/'oci')],check=True)
print(json.dumps({'mode':a.mode,'layout':str(a.output/'oci'),'guest_guard':'SMBIOS serial field-edge-fault-test'}))
