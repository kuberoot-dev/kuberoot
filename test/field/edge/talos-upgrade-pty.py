#!/usr/bin/env python3
"""Give native talosctl a real PTY so its real layer percentages are emitted."""
import fcntl,os,pty,select,signal,struct,subprocess,sys,termios
master,slave=pty.openpty();fcntl.ioctl(slave,termios.TIOCSWINSZ,struct.pack('HHHH',30,110,0,0))
p=subprocess.Popen(sys.argv[1:],stdin=subprocess.DEVNULL,stdout=slave,stderr=slave,env=dict(os.environ,TERM='xterm-256color'),start_new_session=True);os.close(slave)
def stop(signum,frame):
 if p.poll() is None:os.killpg(p.pid,signal.SIGTERM)
signal.signal(signal.SIGTERM,stop);signal.signal(signal.SIGINT,stop)
try:
 while True:
  if select.select([master],[],[],.5)[0]:
   try:data=os.read(master,65536)
   except OSError:break
   if not data:break
   sys.stdout.buffer.write(data);sys.stdout.buffer.flush()
  elif p.poll() is not None:break
finally:
 os.close(master)
 if p.poll() is None:p.terminate()
sys.exit(p.wait())
