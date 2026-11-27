#!/bin/bash
source /work/field/lib.sh
show 'date -u +%FT%TZ'
show 'python3 /work/field/R2-upgrade-trial.py 0.1.1-router.field.1-noforward --timeout 180'
show 'curl -s http://127.0.0.1:8090/; printf "\n"'
show 'date -u +%FT%TZ'
