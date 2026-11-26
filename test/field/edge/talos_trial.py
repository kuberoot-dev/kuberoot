"""Native pinned Talos CLI helpers; private configuration never printed."""
import json,os,pathlib,shlex,subprocess
ROOT=pathlib.Path('/work/field-edge');S=pathlib.Path(__file__).resolve().parent
BASE=['talosctl','--talosconfig',str(ROOT/'talos/config/talosconfig'),'--endpoints','127.0.0.1:15000','--nodes','10.78.0.11']
def call(*args,timeout=15,binary=False):
 r=subprocess.run(BASE+list(args),text=not binary,capture_output=True,timeout=timeout)
 if r.returncode:raise RuntimeError(r.stderr.decode(errors='replace') if binary else r.stderr)
 return r.stdout
def configure(directory):
 private=pathlib.Path(directory)/'registry-config.private.yaml'
 subprocess.run(['talosctl','machineconfig','patch',str(ROOT/'talos/config/controlplane-fixed.yaml'),'--patch','@'+str(ROOT/'oci-registry/talos-registry-patch.json'),'--output',str(private)],check=True,capture_output=True)
 private.chmod(0o600)
 print('$ '+shlex.join(BASE+['apply-config','--mode','no-reboot','--file',str(private)]),flush=True)
 call('apply-config','--mode','no-reboot','--file',str(private),timeout=60)
def boot_id():return call('read','/proc/sys/kernel/random/boot_id').strip()
def boot_default():
 raw=call('read','/sys/firmware/efi/efivars/LoaderEntryDefault-4a67b082-0a4c-41cf-b6c7-440b29bb8c4f',binary=True)
 return raw[4:].decode('utf-16-le').rstrip('\0')
def upgrade(directory,no_reboot=True):
 image=json.loads((ROOT/'talos-good/image.json').read_text())['reference']
 args=BASE+['upgrade','--image',image,'--progress','plain','--reboot-mode','powercycle','--timeout','20m']
 if no_reboot:args+=['--no-reboot']
 print('$ '+shlex.join(args),flush=True)
 return subprocess.Popen(args,stdout=(pathlib.Path(directory)/'talos-upgrade-progress.txt').open('a'),stderr=subprocess.STDOUT)
def counter(reset=False):
 command='sudo nft -j list table inet field_transfer'
 if reset:
  command='sudo nft delete table inet field_transfer 2>/dev/null || true; sudo nft add table inet field_transfer; sudo nft add chain inet field_transfer forwarded "{ type filter hook forward priority 0; policy accept; }"; sudo nft add rule inet field_transfer forwarded ip daddr 10.78.0.11 tcp sport 15005 counter'
 r=subprocess.run([str(S/'gateway-command.sh'),command],text=True,capture_output=True,timeout=20,check=True)
 if reset:return 0
 obj=json.loads(r.stdout);return sum(e.get('counter',{}).get('bytes',0) for row in obj['nftables'] if 'rule' in row for e in row['rule'].get('expr',[]))
