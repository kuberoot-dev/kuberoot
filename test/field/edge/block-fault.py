#!/usr/bin/env python3
"""QEMU-only IOPS/bandwidth and live blkdebug EIO; requires the fault block graph."""
import argparse,json
from qmp import command
p=argparse.ArgumentParser();p.add_argument('qmp');p.add_argument('action',choices=['100-iops','20-iops','unthrottle','eio-on','eio-off']);a=p.parse_args()
if a.action in ['100-iops','20-iops','unthrottle']:
 limits={'iops-total':100 if a.action=='100-iops' else 20 if a.action=='20-iops' else 0,'bps-total':5*1024**2 if a.action!='unthrottle' else 0}
 print(json.dumps(command(a.qmp,'qom-set',{'path':'/objects/field-throttle','property':'limits','value':limits})))
elif a.action=='eio-on':
 print(json.dumps(command(a.qmp,'blockdev-add',{'driver':'blkdebug','node-name':'fault','image':'physical','inject-error':[{'event':'write_aio','iotype':'write','errno':5,'immediately':True}]})))
 print(json.dumps(command(a.qmp,'blockdev-reopen',{'options':[{'driver':'raw','node-name':'disk','file':'fault'}]})))
else:
 print(json.dumps(command(a.qmp,'blockdev-reopen',{'options':[{'driver':'raw','node-name':'disk','file':'physical'}]})))
 print(json.dumps(command(a.qmp,'blockdev-del',{'node-name':'fault'})))
print(json.dumps(command(a.qmp,'query-named-block-nodes'),indent=2))
