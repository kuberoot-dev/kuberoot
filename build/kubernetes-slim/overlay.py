#!/usr/bin/env python3
"""Apply the small, fail-closed overlay to the pinned Kubernetes sources."""
from pathlib import Path
import sys
root = Path(sys.argv[1])
p = root / 'staging/src/k8s.io/apiserver/pkg/server/signal.go'
s = p.read_text()
old = '''func SetupSignalContext() context.Context {
\tclose(onlyOneSignalHandler) // panics when called twice'''
new = '''var sharedSignalContext = sync.OnceValue(setupSignalContext)

func SetupSignalContext() context.Context { return sharedSignalContext() }

func setupSignalContext() context.Context {
\tclose(onlyOneSignalHandler)'''
if old not in s:
    raise SystemExit('signal.go no longer matches the reviewed overlay')
s = s.replace('"os/signal"', '"os/signal"\n"sync"').replace(old, new)
p.write_text(s)
