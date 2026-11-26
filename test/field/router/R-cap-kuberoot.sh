#!/bin/bash
source /work/field/lib.sh
show 'date -u +%FT%TZ'
show 'export KUBECONFIG=/work/cluster-r1.conf'
show 'kubectl api-resources --api-group router.kuberoot.dev'
show 'python3 /work/field/R-cap-kuberoot.py'
show 'kubectl get crd interfaces.router.kuberoot.dev -o jsonpath="{.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties}"; printf "\n"'
show 'date -u +%FT%TZ'
