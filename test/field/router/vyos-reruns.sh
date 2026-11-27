#!/bin/bash
# Reruns after fixing the observer start (R8), the op-mode wrapper (R3) and
# the image server port (R2).
set -uo pipefail
cd /work/field
scene() { local name=$1; shift; printf "#!/bin/bash\nsource /work/field/lib.sh\n%s\nshow %q\n" "${ENVLINE:-}" "$*" > /work/field/scene-$name.sh; DEFER_RENDER=1 bash /work/field/capture.sh "$name" /work/field/scene-$name.sh > raw/$name.capture.log 2>&1; echo "$name exit $(cat casts/$name.exit 2>/dev/null)"; }
scene t-R8-vyos-run2 "python3 /work/field/R8-vyos.py"
scene t-R3-vyos-rule-drift "python3 /work/field/R3-vyos-rule-drift.py"
ENVLINE="export R3_PHASES=writers,replay R3_SUMMARY=R3-vyos-run2.summary.json" scene t-R3-vyos-run2 "python3 /work/field/R3-vyos.py"
scene t-R2-vyos-run2 "python3 /work/field/R2-vyos-upgrade.py"
echo vyos-run2-done
