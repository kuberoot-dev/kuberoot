#!/bin/bash
source /work/field/lib.sh
show 'date -u +%FT%TZ'
show 'python3 /work/field/R1-address.py'
show 'date -u +%FT%TZ'
