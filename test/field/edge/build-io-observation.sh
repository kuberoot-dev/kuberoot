#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/lib.sh"
mkdir -p "$FIELD_ROOT/io-observation"
# Never replace a mapped library in place.
gcc -Wall -Wextra -O2 -shared -fPIC -pthread "$FIELD_SCRIPTS/qemu-io-diagnostic.c" -ldl -o "$FIELD_ROOT/io-observation/qemu-io-observation.so.next"
mv "$FIELD_ROOT/io-observation/qemu-io-observation.so.next" "$FIELD_ROOT/io-observation/qemu-io-observation.so"
sha256sum "$FIELD_SCRIPTS/qemu-io-diagnostic.c" "$FIELD_ROOT/io-observation/qemu-io-observation.so" > "$FIELD_ROOT/io-observation/build.sha256"
