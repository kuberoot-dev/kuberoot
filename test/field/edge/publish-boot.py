#!/usr/bin/env python3
"""Publish a real completed repetition, with an explicit artifact transfer map."""
import json,pathlib,sys,subprocess
product,rep,name=sys.argv[1:]
root=pathlib.Path('/work/field-edge');d=root/'results'/('E-B2-'+product)/rep
r=json.loads((d/'result.json').read_text());transfers={}
for ext in ['gif','png','txt','cast','timing.json','verification.json']:
 filename=name+('-verification.json' if ext=='verification.json' else '.'+ext)
 source=root/'captures'/filename
 target='02-edge/'+('img/' if ext in ['gif','png'] else 'raw/')+filename
 transfers[target]=str(source)
for filename in ['serial.log','result.json']:
 transfers['02-edge/raw/'+name+'-'+filename]=str(d/filename)
if rep=='1':
 transfers['02-edge/img/'+name+'-serial.gif']=str(d/'serial.gif')
 transfers['02-edge/raw/'+name+'-serial.cast']=str(d/'serial.cast')
if (d/'vga/console.gif').exists():
 transfers['02-edge/img/'+name+'-vga.gif']=str(d/'vga/console.gif')
 transfers['02-edge/img/'+name+'-vga.png']=str(d/'vga/console.png')
 transfers['02-edge/raw/'+name+'-vga-timing.json']=str(d/'vga/timing.json')
r['artifact_transfers']=transfers;r['artifact_paths']=list(transfers)
with (root/'results.jsonl').open('a') as f:f.write(json.dumps(r)+'\n')
