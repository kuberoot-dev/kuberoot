#!/usr/bin/env bash
# CAPI is installed by its provider package, not as a host distribution add-on.
set -euo pipefail
cd "$(dirname "$0")/.."

go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.22.0 \
  object:headerFile=hack/boilerplate.go.txt \
  paths=./pkg/apis/infrastructure/... \
  crd:crdVersions=v1 output:crd:dir=config/capi/crd
