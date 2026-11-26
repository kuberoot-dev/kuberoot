#!/usr/bin/env python3
"""Render explicitly labelled projections of saved real E2 observations."""
import json,pathlib,subprocess,sys
root=pathlib.Path(sys.argv[1]);scripts=pathlib.Path(__file__).resolve().parent
record=json.loads((root/'result.json').read_text())
for phase in ['before','after']:
 source=root/f'bootentries-{phase}.json'
 if not source.is_file():continue
 captured=json.loads(source.read_text())
 projection=[{'name':item['metadata']['name'],'status':item['status']} for item in captured['items']]
 text=f"E2 / {record['product']} / {record['fault_phase']}\nSaved BootEntry data: {phase}\nSource: {source.name}\n\n"+json.dumps(projection,indent=2)+'\n'
 output=root/f'bootentries-{phase}-view.txt';output.write_text(text)
 subprocess.run(['python3',str(scripts/'terminal-png.py'),str(output),str(root/f'bootentries-{phase}-view.png'),'--last-lines','60'],check=True)
metrics=record.get('metrics',{})
lines=['E2 / saved measurement summary',record['product'],f"Case: {record['fault_phase']} / repetition {record.get('repetition')}",f"Protocol: {record.get('measurement_protocol')}",f"UTC: {record['time_utc']}",f"Status: {record['status']}",'']
for label,key in [('Automatic OS rollback, s','automatic_rollback_seconds'),('Measured attempt budget, s','rollback_budget_seconds'),('Post-outcome observation, s','post_terminal_observation_seconds'),('Fresh 60 s service window','sustained_service_window_observed'),('ACK records','acknowledged'),('Recovered records','recovered'),('ACK records lost','acknowledged_record_loss'),('Corrupt records','corrupt_records'),('HTTP failures in final 60 s','final_60s_http_failures'),('Append failures in final 60 s','final_60s_append_failures'),('API failures in final 60 s','final_60s_api_failures')]:
 if key in metrics:
  value=metrics[key]
  if isinstance(value,float):value=f'{value:.3f}'
  lines.append(f'{label}: {value}')
lines+=['','Reason: '+record['reason'],'','Source: result.json (complete values retained)']
output=root/'measurement-summary.txt';output.write_text('\n'.join(lines)+'\n')
subprocess.run(['python3',str(scripts/'terminal-png.py'),str(output),str(root/'measurement-summary.png'),'--last-lines','60'],check=True)
