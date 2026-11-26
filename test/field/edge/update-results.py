#!/usr/bin/env python3
"""Publish completed machine records; never turn a pending row into unsupported.
Runs on Mac for Markdown/JSON editing only; execution/plotting stays in pod.
"""
import argparse,datetime,json,pathlib
p=argparse.ArgumentParser();p.add_argument('--article-root',type=pathlib.Path,required=True);p.add_argument('--record',type=pathlib.Path,required=True);a=p.parse_args()
root=a.article_root;record=json.loads(a.record.read_text());db=root/'results/edge.jsonl'
records=[json.loads(x) for x in db.read_text().splitlines() if x.strip()]
identity=lambda r:(r.get('scenario_id'),r.get('product'),r.get('time_utc'),r.get('repetition'))
if identity(record) not in {identity(r) for r in records}:
 with db.open('a') as f:f.write(json.dumps(record,ensure_ascii=False)+'\n')
 records.append(record)
method='Все продукты используют одинаковые непривилегированные L2-сегменты QEMU socket multicast. NAT, netem/tbf, IFB и blackhole выполняются внутри Linux gateway VM; NET_ADMIN/TUN в pod не нужны. Прикладные пробы проходят через DUT, SSH/user-net служит управлению. Forwarding сравнивается относительно измеренного потолка этой сети, не как line rate. Диски ограничиваются QEMU throttle groups, ENOSPC создаётся внутри гостевого state, EIO — blkdebug. Power cut останавливает QEMU, RTC задаётся на VM. Прежний отказ admission — история, а не текущая блокировка.'
lines=['# Edge: результаты полевой серии','','Серия ещё выполняется. Phase 1 и проверки harness не засчитаны как результаты новых тестов.','',method,'']
ids=['E-B1','E-B2','E-B3']+[f'E{i}' for i in range(1,11)]
for ident in ids:
 lines+=['## '+ident,'']
 selected=[r for r in records if r.get('scenario_id')==ident]
 if not selected:lines+=['Не запущен; ожидает своей очереди.',''];continue
 for r in selected:
  lines+=[f"### {r['product']}",'',f"Статус: **{r.get('status','не указан')}**. {r.get('reason','')}",'']
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
with (root/'02-edge/STAND_LOG.md').open('a') as f:
 f.write(f"\n## {datetime.datetime.now(datetime.timezone.utc).isoformat()} — {record['scenario_id']} / {record['product']}\n\n- Статус: {record.get('status')}. {record.get('reason','')}\n- Машинная запись добавлена в results/edge.jsonl, RESULTS.md обновлён.\n")
