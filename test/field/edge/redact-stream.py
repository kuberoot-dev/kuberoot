#!/usr/bin/env python3
"""Only redact secret query strings/age private keys; never synthesize output.
The unredacted evidence stays inside the owned PVC, excluded from publication.
"""
import re,sys
for line in sys.stdin:
 line=re.sub(r'(https?://[^\s"\']+)\?[^\s"\']+',r'\1?[private signed query]',line)
 line=re.sub(r'AGE-SECRET-KEY-[A-Z0-9]+','[private age identity]',line)
 print(line,end='',flush=True)
