#!/usr/bin/env python3
"""Publish completed machine records; never turn a pending row into unsupported.
Runs on Mac for Markdown/JSON editing only; execution/plotting stays in pod.
"""
import argparse,datetime,json,pathlib,math,statistics
p=argparse.ArgumentParser();p.add_argument('--article-root',type=pathlib.Path,required=True);p.add_argument('--record',type=pathlib.Path,required=True);a=p.parse_args()
root=a.article_root;record=json.loads(a.record.read_text());db=root/'results/edge.jsonl'
records=[json.loads(x) for x in db.read_text().splitlines() if x.strip()]
identity=lambda r:(r.get('scenario_id'),r.get('product'),r.get('time_utc'),r.get('repetition'))
added=identity(record) not in {identity(r) for r in records}
if added:
 with db.open('a') as f:f.write(json.dumps(record,ensure_ascii=False)+'\n')
 records.append(record)
method='Все продукты используют одинаковые непривилегированные L2-сегменты QEMU socket multicast. NAT, netem/tbf, IFB и blackhole выполняются внутри Linux gateway VM; NET_ADMIN/TUN в pod не нужны. Прикладные пробы проходят через DUT, SSH/user-net служит управлению. Forwarding сравнивается относительно измеренного потолка этой сети, не как line rate. Диски ограничиваются QEMU throttle groups, ENOSPC создаётся внутри гостевого state, EIO — blkdebug. Power cut останавливает QEMU, RTC задаётся на VM. Прежний отказ admission — история, а не текущая блокировка.'
lines=['# Edge: результаты полевой серии','','Серия ещё выполняется. Phase 1 и проверки harness не засчитаны как результаты новых тестов.','',method,'']
ids=['E-B1','E-B2','E-B3']+[f'E{i}' for i in range(1,11)]
for ident in ids:
 lines+=['## '+ident,'']
 selected=[r for r in records if r.get('scenario_id')==ident]
 if not selected:lines+=['Не запущен; ожидает своей очереди.',''];continue
 if ident=='E-B1':
  lines+=['| Продукт | Samples | RAM usage / working set, MiB | CPU, cores | Reported guest FS, MiB | Host raw, MiB | Writes, MiB/h |','|---|---:|---:|---:|---:|---:|---:|']
  def metric(r,key,scale=1):
   v=r.get('metrics',{}).get(key);return '—' if v is None else (f'{v/scale:.3f}' if key=='cpu_cores' else f'{v/scale:.2f}')
  for r in selected:
   lines.append(f"| {r['product']} | {r.get('samples_ok','—')}/{r.get('samples_total','—')} | {metric(r,'guest_memory_usageBytes_median',2**20)} / {metric(r,'guest_memory_workingSetBytes_median',2**20)} | {metric(r,'cpu_cores')} | {metric(r,'guest_filesystem_used_bytes',2**20)} | {metric(r,'raw_disk_host_allocated_bytes',2**20)} | {metric(r,'virtual_block_write_bytes_per_hour',2**20)} |")
  lines+=['','Reported guest FS — filesystem из kubelet stats: у kuberoot это state, у других продуктов область может отличаться. Host raw показывает весь sparse disk; подробности ниже.','']
 if ident=='E-B2':
  lines+=['| Продукт | Запусков / ошибок | API median / p95, s | Fresh Ready median / p95, s | DNS median / p95, s | New pod median / p95, s |','|---|---:|---:|---:|---:|---:|']
  for product in dict.fromkeys(r['product'] for r in selected):
   all_runs=[r for r in selected if r['product']==product]
   runs=[r for r in all_runs if r.get('measurement_protocol')=='boot-readiness-v2'] or all_runs
   def pair(key):
    values=sorted(r['metrics'][key] for r in runs if key in r.get('metrics',{}))
    return '—' if not values else f'{statistics.median(values):.2f} / {values[max(0,math.ceil(.95*len(values))-1)]:.2f}'
   values=[pair(k) for k in ['api_ready_seconds','node_ready_fresh_heartbeat_seconds','coredns_answer_seconds','new_pod_serves_seconds']]
   lines.append('| '+product+' | '+str(len(runs))+' / '+str(sum(r.get('status')=='failed' for r in runs))+' | '+' | '.join(values)+' |')
  lines+=['','p95 — nearest rank; десять повторов дают слабую оценку хвоста. Таблица использует исправленный boot-readiness-v2, если он есть; ранние обрывы старого драйвера сохранены ниже и не смешиваются с новой выборкой. Все неудачи и измеренные этапы сохранены по отдельности.','']
 for r in selected:
  label=r['product']+(f", repetition {r['repetition']}" if 'repetition' in r else '')
  lines+=[f"### {label}",'',f"Статус: **{r.get('status','не указан')}**. {r.get('reason','')}",'']
  if r.get('metrics'):
   lines+=['| Метрика | Измерение |','|---|---|']
   for key,val in r['metrics'].items():lines.append(f'| `{key}` | {val if val is not None else "не измерено"} |')
   lines+=['']
  lines+=[f"Источник: `{r.get('source_commit','не указан')}`; образ: `{r.get('image_digest','не указан')}`.",'']
  for note in r.get('measurement_notes',[]): lines += [note,'']
  for artifact in r.get('artifact_paths',[]):lines.append(f'- `{artifact}`')
  lines+=['']
lines+=['## Применимость','','Выводы об отказоустойчивости и сравнении продуктов требуют завершённых отказных испытаний; baseline сам по себе их не доказывает.','']
(root/'02-edge/RESULTS.md').write_text('\n'.join(lines))
if added:
 with (root/'02-edge/STAND_LOG.md').open('a') as f:
  f.write(f"\n## {datetime.datetime.now(datetime.timezone.utc).isoformat()} — {record['scenario_id']} / {record['product']}\n\n- Статус: {record.get('status')}. {record.get('reason','')}\n- Машинная запись добавлена в results/edge.jsonl, RESULTS.md обновлён.\n")
