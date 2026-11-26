#!/bin/bash
set -euo pipefail
export PATH=/work/bin:$PATH TERM=xterm-256color LC_ALL=C.UTF-8
show() {
 python3 - "$1" <<'PY'
import sys,time
print('\033[1;32m$ \033[0m',end='',flush=True)
for c in sys.argv[1]:
 print(c,end='',flush=True);time.sleep(.018)
print()
PY
 eval "$1"
 sleep 1
}
