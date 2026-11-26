#!/bin/bash
# Resume collection after the original scene exited on a dead Kubernetes API.
# WAN stays blackholed for the full required hour. No DUT setting is repaired.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
D=$FIELD_ROOT/results/${RUN_NAME:?};G=$FIELD_SCRIPTS/gateway-command.sh
(( $(date +%s) >= $(cat "$D/fault-epoch")+3600 )) || exit 2
show export "KUBECONFIG=$KUBECONFIG"
show date -u
python3 - "$D" <<'PY'
import json,os,pathlib,shlex,subprocess,sys,time
p=pathlib.Path(sys.argv[1]);cmd=['kubectl','--request-timeout=5s','-n','field','get','pod','uncached-image','-o','json']
print('$ ',end='',flush=True)
for ch in shlex.join(cmd):print(ch,end='',flush=True);time.sleep(.012)
print(flush=True);r=subprocess.run(cmd,text=True,capture_output=True,timeout=10)
print(r.stdout+r.stderr,end='',flush=True)
(p/'missing-image-error.txt').write_text(r.stderr)
if r.returncode==0:(p/'missing-image.json').write_text(r.stdout)
(p/'continuation.json').write_text(json.dumps({'reason':'original scene exited while Kubernetes API was unavailable; original failed capture retained','missing_image_query_exit':r.returncode,'wan_reopened_only_after_full_hour':True,'guest_configuration_changed':False,'observation_utc_epoch':time.time()},indent=2))
PY
if [[ $PRODUCT == talos* ]];then
 show "$G" '/home/observer/talosctl --talosconfig /home/observer/talosconfig --nodes 10.78.0.11 --endpoints 10.78.0.11 services'
 show "$G" '/home/observer/talosctl --talosconfig /home/observer/talosconfig --nodes 10.78.0.11 --endpoints 10.78.0.11 get timestatus -o yaml'
fi
show "$FIELD_SCRIPTS/wan-impairment.sh" clean
date +%s > "$D/wan-restored-epoch"
end=$(( $(date +%s)+340 ))
while (( $(date +%s)<end ));do
 remain=$((end-$(date +%s)));((remain>30)) && remain=30
 ((remain>0)) && sleep "$remain"
 show date -u
done
if [[ $PRODUCT == talos* ]];then
 show "$G" '/home/observer/talosctl --talosconfig /home/observer/talosconfig --nodes 10.78.0.11 --endpoints 10.78.0.11 services'
 show "$G" '/home/observer/talosctl --talosconfig /home/observer/talosconfig --nodes 10.78.0.11 --endpoints 10.78.0.11 get timestatus -o yaml'
fi
if show kubectl --request-timeout=5s get --raw /readyz;then :;fi
bash "$FIELD_SCRIPTS/collect-wan-evidence.sh"
