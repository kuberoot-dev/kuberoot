"""Validate every published animation, including auxiliary console captures."""
from pathlib import Path
from PIL import Image

def inspect(path, artifact=None):
 path=Path(path)
 with Image.open(path) as picture:
  duration=0
  for frame in range(picture.n_frames):
   picture.seek(frame);duration+=picture.info.get('duration',0)/1000
  row={'artifact':artifact or str(path),'bytes':path.stat().st_size,'frames':picture.n_frames,'duration_seconds':duration,'pixels':picture.size}
 if not (row['bytes']<8*1024**2 and 15<=duration<=60):raise ValueError('GIF outside capture limits: '+str(row))
 return row
