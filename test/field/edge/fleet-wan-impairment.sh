#!/bin/bash
set -euo pipefail
id=${1:?1..3};mode=${2:?clean|lossy|blackhole};[[ $id =~ ^[123]$ ]] || exit 2
export GATEWAY_SSH_PORT=2230$id GATEWAY_KNOWN_HOSTS=/work/field-edge/fleet-field/gateway-known-hosts
exec bash /work/field-edge/scripts/wan-impairment.sh "$mode"
