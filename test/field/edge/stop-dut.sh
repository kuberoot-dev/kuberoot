#!/bin/bash
# Graceful ACPI shutdown before saving a cold-boot baseline. Never kills silently.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
d=${1:?owned VM directory}
[[ $d == "$FIELD_ROOT/"* ]] || exit 2
pid=$(cat "$d/pid")
python3 "$FIELD_SCRIPTS/qmp.py" "$d/qmp.sock" system_powerdown
for attempt in $(seq 1 120); do
 if ! test -e "/proc/$pid/stat" || test "$(awk '{print $3}' "/proc/$pid/stat")" = Z; then
  date -u
  exit 0
 fi
 sleep 1
done
printf 'ACPI shutdown did not finish in 120 seconds; disk not declared clean\n' >&2
exit 1
