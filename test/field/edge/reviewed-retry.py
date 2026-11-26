#!/usr/bin/env python3
"""Resolve explicit preserved invalid-fixture retry markers without overwriting."""
import json,pathlib,re,sys
original=pathlib.Path(sys.argv[1]);current=original;seen=set();attempt=1
while (current/'retry.json').exists():
 assert current not in seen;seen.add(current)
 record=json.loads((current/'result.json').read_text())
 assert record['status']=='invalid-fixture' and (current/'published').is_file()
 review=json.loads((current/'retry.json').read_text());name=review['directory']
 match=re.fullmatch(re.escape(original.name)+r'-attempt([2-9][0-9]*)',name)
 assert review['reason'].strip() and match and int(match[1])==attempt+1
 assert (current/'failed.qcow2').is_file() and not (current/'system.raw').exists()
 attempt=int(match[1]);current=original.with_name(name)
print(current)
