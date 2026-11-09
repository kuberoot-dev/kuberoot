#!/usr/bin/env bash
# Run code generation on the build host before packaging provider components.
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ ${SKIP_GENERATION:-0} != 1 ]]; then
  hack/update-capi-codegen.sh
fi
output=${1:-config/capi/infrastructure-components.yaml}
mkdir -p "$(dirname "$output")"
tmp=$(mktemp "${output}.XXXXXX")
trap 'rm -f "$tmp"' EXIT
for name in kuberootclusters kuberootmachines kuberootmachinetemplates; do
  crd="config/capi/crd/infrastructure.cluster.x-k8s.io_${name}.yaml"
  test -s "$crd"
  cat "$crd" >> "$tmp"
done
printf '\n---\n' >> "$tmp"
cat config/capi/manager.yaml >> "$tmp"
mv "$tmp" "$output"
