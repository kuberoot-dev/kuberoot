#!/usr/bin/env python3
"""Resolve an explicitly reviewed invalid-fixture retry without replacing evidence."""
import json,pathlib,re,sys
original=pathlib.Path(sys.argv[1]);record=json.loads((original/'result.json').read_text())
assert record['status']=='invalid-fixture' and (original/'published').is_file()
review=json.loads((original/'retry.json').read_text());name=review['directory']
assert review['reason'].strip() and re.fullmatch(re.escape(original.name)+r'-attempt[2-9][0-9]*',name)
assert (original/'failed.qcow2').is_file() and not (original/'system.raw').exists()
print(original.with_name(name))
