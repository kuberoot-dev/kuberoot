#!/usr/bin/env python3
"""Capture real QEMU VGA frames for guests whose default console is tty0.
Keep actual UTC/monotonic timing in JSON; retime only playback, never pixels.
"""
import argparse,datetime,hashlib,json,pathlib,time
from PIL import Image
from qmp import command
p=argparse.ArgumentParser();p.add_argument('--qmp',required=True);p.add_argument('--output',type=pathlib.Path,required=True);p.add_argument('--stop-file',type=pathlib.Path,required=True);p.add_argument('--seconds',type=float,default=600);p.add_argument('--interval',type=float,default=1);a=p.parse_args();assert a.interval>=1
a.output.mkdir(parents=True,exist_ok=False);start=time.monotonic();frames=[];events=[];last=None
while time.monotonic()-start<a.seconds and not a.stop_file.exists():
 began=time.monotonic()
 try:
  raw=a.output/'frame.ppm';command(a.qmp,'screendump',{'filename':str(raw)})
  digest=hashlib.sha256(raw.read_bytes()).hexdigest()
  event={'elapsed_seconds':time.monotonic()-start,'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'sha256':digest}
  if digest!=last:
   picture=Image.open(raw).convert('P',palette=Image.Palette.ADAPTIVE,colors=64)
   frames.append((event['elapsed_seconds'],picture.copy()));picture.save(a.output/f'frame-{len(frames):04}.png');last=digest
   event['frame']=len(frames)
  events.append(event)
 except Exception as exc:events.append({'elapsed_seconds':time.monotonic()-start,'error':str(exc)})
 time.sleep(max(0,a.interval-(time.monotonic()-began)))
(a.output/'timing.json').write_text(json.dumps(events,indent=2))
if not frames:raise SystemExit('No VGA frames captured')
end=time.monotonic()-start
pauses=[max(.1,min(1.5,(frames[i+1][0] if i+1<len(frames) else end)-t)) for i,(t,_) in enumerate(frames)]
# Long periods of active scrolling are sped up uniformly after idle capping.
factor=min(1,50/max(sum(pauses),.1));durations=[max(20,round(x*factor*1000/10)*10) for x in pauses]
durations[-1]+=max(5000,15100-sum(durations))
images=[im for _,im in frames];images[0].save(a.output/'console.gif',save_all=True,append_images=images[1:],duration=durations,loop=0,optimize=True)
images[-1].save(a.output/'console.png')
assert (a.output/'console.gif').stat().st_size<8*1024**2,'VGA GIF needs smaller crop or frame selection'
assert 15000<=sum(durations)<=60000
print(json.dumps({'frames':len(frames),'real_seconds':end,'playback_seconds':sum(durations)/1000,'bytes':(a.output/'console.gif').stat().st_size}),flush=True)
