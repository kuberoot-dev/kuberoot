"""Human descriptions of public evidence, without inventing observed outcomes."""
from pathlib import Path

SCENES = {
 'E-B1': '30-минутное измерение idle RAM, CPU и записей на диск',
 'E-B2': 'холодная загрузка, API, свежий Node Ready, DNS и новый pod',
 'E-B3': 'рост числа pod, потребление памяти и задержки API',
 'E1': 'час без WAN, жёсткая перезагрузка и проверка отсутствующего образа',
 'E2': 'обновление с неисправным payload либо проверка подписи и целостности',
 'E3': 'обрыв питания в зафиксированной фазе и наблюдение восстановления',
 'E4': 'введение отказа хранения, работа W и снятие отказа',
 'E5': 'разделение трёх etcd-узлов, перезагрузка лидера и восстановление связности',
 'E6': 'неверные часы без NTP, TLS-диагностика и коррекция времени',
 'E7': 'отказ узла/API-пути, замена узла и проверка прежней идентичности',
 'E8': 'восстановление нового узла из snapshot и проверки ошибочных входных данных',
 'E9': 'передача выпуска через ограниченные окна WAN и прерывание загрузки',
 'E10': 'canary rollout, отказ центра, права на proxy и отзыв сайта',
}

def describe(path, scenario):
 name=Path(path).name
 if 'qmp-events' in name:return 'Настоящие события QMP, включая reset и ошибки block I/O, с UTC получения'
 if 'host-io-syscalls' in name:return 'Метаданные фактических host I/O вызовов: alignment probes и ошибки; без содержимого гостевых данных'
 if 'service-curve-provenance' in name:return 'Источник графика клиентских запросов, интервалы, ACK и временные отметки' 
 if 'storage-diagnosis' in name:return 'E2: фактический preboot Upgrade EIO, исходный слот A и восемь ошибок записи QEMU; целевой fault не достигнут'
 if 'storage-probe' in name:return 'E2: проверка отдельного scratch-файла на PVC через cache=none: восемь write/flush/pattern-read; причина исходного EIO не установлена'
 if 'host-resources' in name:return 'Свободные байты PVC и счётчики памяти pod с UTC каждые пять секунд'
 if name.endswith('.integrity.json'):return 'SHA256 и размеры для проверки точности gzip-архива'
 if 'measurement-summary' in name:return 'Читаемая сводка фактически измеренных E2 величин из result.json'
 if 'bootentries-before-view' in name or 'bootentries-after-view' in name:return 'Проекция сохранённого BootEntry с указанием исходного файла: имя слота и полный status'
 if 'serial' in name or 'recovery.cast' in name or 'recovery.gif' in name:return 'Настоящая serial-консоль QEMU при загрузке/восстановлении с видимыми часами'
 if '-vga-' in name:return 'Настоящий VGA framebuffer QEMU и временные отметки'
 if name.startswith('chart-'):return 'График измеренных величин: '+SCENES.get(scenario,scenario)
 if 'gateway-monitor' in name or 'fabric-monitor' in name:return 'Ежесекундные счётчики gateway conntrack: occupancy, limit и drops'
 if 'observer' in name:return 'Исходные внешние HTTP/append/ConfigMap пробы: время, задержки, ошибки и ACK'
 if 'recovered-records' in name or name.endswith(('-records','-records.gz')):return 'Байты durable records, считанные после опыта для сопоставления с ACK'
 if 'durability' in name:return 'Сопоставление ACK с восстановленными записями: loss, corruption и duplicates'
 if 'missing-image' in name:return 'Статус pod и причина отказа загрузки некэшированного образа'
 if 'whole-window-audit' in name:return 'Проверка всех запросов за окно, включая интервалы до reset и после recovery'
 if 'bootentries-before' in name:return 'BootEntry до обновления: выбранный слот, release, state и attempts'
 if 'bootentries-after' in name:return 'BootEntry после опыта: выбранный слот, release, state и attempts'
 if 'fault-epoch' in name or 'power-cut-epoch' in name or 'relief-epoch' in name:return 'Фактическое UTC-время введения или снятия отказа'
 if 'verification' in name:return 'Проверка размера, кадров и продолжительности screencast'
 if 'timing' in name or 'speed' in name:return 'Исходная и сокращённая длительность записи; параметры сжатия пауз'
 if name.endswith('.gif'):return 'Реальные команды, реакция системы и часы: '+SCENES.get(scenario,scenario)
 if name.endswith('.png'):return 'Последние 30 строк настоящего терминального вывода; полный текст в raw'
 if name.endswith('.txt'):return 'Полный текст команд и вывода: '+SCENES.get(scenario,scenario)
 if name.endswith('.cast'):return 'Исходный asciinema-поток с настоящими временными отметками'
 if 'recovery.json' in name:return 'Время первого 60-секундного непрерывного восстановления и счётчики запросов'
 return 'Исходное публичное свидетельство '+scenario+': '+name
