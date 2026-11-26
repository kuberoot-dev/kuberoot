#!/usr/bin/env python3
"""Release only stopped Phase-1 installer media with verified retained payloads.
Guest disks, trust directories, captured evidence, and source artifacts stay.
"""
import datetime,hashlib,json,pathlib,shlex,subprocess
root=pathlib.Path('/work/field-edge');out=root/'reproducible-media-manifest.json';rows=json.loads(out.read_text()) if out.exists() else []
paths=[*pathlib.Path('/work/media-demo').glob('*.img'),*pathlib.Path('/work/stand').glob('*.img'),*pathlib.Path('/work/fleet').glob('*.img')]
def digest_file(path):
 with path.open('rb') as f:return hashlib.file_digest(f,'sha256').hexdigest()
def read_media(path,name,hash_only=False):
 cmd=['mcopy','-i',str(path)+'@@1048576','::/'+name,'-']
 if not hash_only:return subprocess.check_output(cmd,text=True)
 proc=subprocess.Popen(cmd,stdout=subprocess.PIPE);h=hashlib.sha256()
 while chunk:=proc.stdout.read(8*1024**2):h.update(chunk)
 assert proc.wait()==0;return h.hexdigest()
def unattached(path):
 for proc in pathlib.Path('/proc').glob('[0-9]*/cmdline'):
  try:args=proc.read_bytes().decode().split('\0')
  except (OSError,UnicodeError):continue
  if args[0].endswith('qemu-system-x86_64') and any(str(path) in arg for arg in args):raise RuntimeError('media is attached: '+str(path))
for path in paths:
 assert not path.is_symlink();unattached(path)
 entry=read_media(path,'loader/entries/kuberoot-install.conf')
 fields=dict(line.split(None,1) for line in entry.splitlines() if line.strip());version=fields['version']
 artifacts=pathlib.Path('/work/artifacts' if version=='0.1.0' else '/work/series/'+version);assert artifacts.is_dir()
 hashes={}
 for filename in ['rootfs.squashfs','vmlinuz.efi','initrd.cpio']:
  expected=digest_file(artifacts/filename);actual=read_media(path,'kuberoot/'+filename,True);assert actual==expected,(str(path),filename);hashes[filename]=actual
 actual=read_media(path,'EFI/BOOT/BOOTX64.EFI',True);assert actual==digest_file(artifacts/'systemd-boot.efi');hashes['systemd-boot.efi']=actual
 trust=path.parent/'trust';assert (trust/'admin.kubeconfig').is_file()
 for filename in ['serving-ca.crt','serving-ca.key','admin-ca.crt']:
  assert read_media(path,'kuberoot/trust/'+filename,True)==digest_file(trust/filename)
 args=[v for v in shlex.split(fields['options']) if v not in ['quiet','console=tty0','console=ttyS0','kuberoot.mode=install'] and not v.startswith('kuberoot.root=')]
 recipe=['mkimage','-arch','amd64','-artifacts',str(artifacts),'-version',version,'-trust',str(trust),'-out',str(path),'-args',' '.join(args)]
 row={'path':str(path),'logical_bytes':path.stat().st_size,'host_allocated_bytes':path.stat().st_blocks*512,'sha256':digest_file(path),'retained_artifacts':str(artifacts),'payload_sha256':hashes,'retained_trust':str(trust),'loader_entry':entry,'recreate_command':shlex.join(recipe),'removed':False,'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'note':'Recreated media has fresh FAT/GPT metadata; payload and trust remain identical. Installed VM disks are untouched.'}
 rows.append(row);out.write_text(json.dumps(rows,indent=2));unattached(path);path.unlink();row['removed']=True;out.write_text(json.dumps(rows,indent=2));print(json.dumps({k:row[k] for k in ['path','host_allocated_bytes','removed','recreate_command']}),flush=True)
