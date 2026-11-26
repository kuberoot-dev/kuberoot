#!/usr/bin/env python3
"""Wait for a scene error, retain it, and finish planned WAN relief/collection.
This never shortens the hour or changes the DUT configuration. On normal E1
completion it exits without intervening. The original queue is already done
before this continuation starts, so no competing DUT is launched.
"""
import argparse,json,os,pathlib,subprocess,time
p=argparse.ArgumentParser();p.add_argument('--parent',type=int,required=True);p.add_argument('--product',choices=['talos'],required=True);a=p.parse_args()
root=pathlib.Path('/work/field-edge');s=pathlib.Path(__file__).resolve().parent;name='E1-gateway-v2-talos-v1.14.7';d=root/'results'/name;capture=root/'captures'/('t-'+name)
while not pathlib.Path(str(capture)+'.exit').exists():time.sleep(2)
code=int(pathlib.Path(str(capture)+'.exit').read_text())
if code==0:print('Original E1 scene completed; continuation unnecessary',flush=True);raise SystemExit(0)
assert time.time()>=float((d/'fault-epoch').read_text())+3600,'scene failed before mandatory hour; needs fixture diagnosis'
while pathlib.Path(f'/proc/{a.parent}/stat').exists():
 try:state=pathlib.Path(f'/proc/{a.parent}/stat').read_text().split()[2]
 except FileNotFoundError:break
 if state=='Z':break
 time.sleep(2)
assert not (d/'result.json').exists(),'completed result must not be overwritten'
print('Preserving failed scene; continuing scheduled relief and evidence collection',flush=True)
(d/'supplementary-captures.json').write_text(json.dumps(['t-E1-talos-offline-time-sync','t-E1-talos-after-wan-restored']))
command=r'''
set -euo pipefail
source /work/field-edge/scripts/lib.sh
source /work/field-edge/scripts/select-product.sh talos
export WAN_RUN_LABEL=gateway-v2 RUN_NAME=E1-gateway-v2-talos-v1.14.7 VM_DIR=/work/field-edge/wan-gateway-v2-talos
bash "$FIELD_SCRIPTS/capture.sh" t-E1-talos-after-wan-restored "$FIELD_SCRIPTS/E1-continue-after-api-failure.sh"
bash "$FIELD_SCRIPTS/render-existing-capture.sh" t-E1-talos-offline-time-sync
python3 "$FIELD_SCRIPTS/verify-capture.py" "$FIELD_ROOT/captures/t-$RUN_NAME" > "$FIELD_ROOT/captures/t-$RUN_NAME-verification.json"
# Stop and preserve BEFORE publishing, so after-wan-prepare cannot overlap a DUT.
bash "$FIELD_SCRIPTS/stop-dut.sh" "$VM_DIR"
qemu-img convert -c -f raw -O qcow2 "$VM_DIR/system.raw" "$VM_DIR/after-fault.qcow2"
qemu-img compare -f raw -F qcow2 "$VM_DIR/system.raw" "$VM_DIR/after-fault.qcow2"
rm -- "$VM_DIR/system.raw"
python3 "$FIELD_SCRIPTS/publish-wan.py"
'''
subprocess.run(['bash','-c',command],check=True)
print('Failed DUT outcome published with original and continuation evidence',flush=True)
