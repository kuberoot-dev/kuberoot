"""Passive QMP event collection; no guest or block-device changes."""
import datetime,json,socket,threading

class Recorder:
 def __init__(self,directory):
  self.output=(directory/'qmp-events.jsonl').open('x')
  self.socket=socket.socket(socket.AF_UNIX);self.socket.settimeout(10)
  self.socket.connect(str(directory/'qmp.sock'));self.stream=self.socket.makefile('rwb',buffering=0)
  self.write({'greeting':json.loads(self.stream.readline())})
  self.stream.write(b'{"execute":"qmp_capabilities","id":"event-recorder"}\n')
  while True:
   response=json.loads(self.stream.readline())
   if response.get('event'):self.write(response)
   if response.get('id')=='event-recorder':
    if 'error' in response:raise RuntimeError(response['error'])
    break
  self.socket.settimeout(None);self.thread=threading.Thread(target=self.read,daemon=True);self.thread.start()
 def write(self,event):
  self.output.write(json.dumps({'received_utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'qmp':event})+'\n');self.output.flush()
 def read(self):
  try:
   while line:=self.stream.readline():
    event=json.loads(line)
    if event.get('event'):self.write(event)
  except (OSError,ValueError) as exc:self.write({'recorder_error':str(exc)})
 def close(self):
  self.thread.join(timeout=2)
  if self.thread.is_alive():
   try:self.socket.shutdown(socket.SHUT_RDWR)
   except OSError:pass
   self.thread.join(timeout=2)
  self.stream.close();self.socket.close();self.output.close()
