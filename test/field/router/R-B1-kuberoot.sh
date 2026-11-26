#!/bin/bash
source /work/field/lib.sh
show 'date -u +%FT%TZ'
show 'export KUBECONFIG=/work/trust/admin.kubeconfig'
show 'kubectl --server https://127.0.0.1:5020 get osconfigs -o yaml'
show "kubectl --server https://127.0.0.1:5020 get nodeservices -o custom-columns=SERVICE:.metadata.name,STATE:.status.state,CGROUP_BYTES:.status.memoryBytes"
show 'python3 /work/field/qmp.py r2 query-blockstats'
show 'du -B1 /work/vms/r2/sys.qcow2'
show 'date -u +%FT%TZ'
