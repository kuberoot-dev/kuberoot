#!/usr/bin/env bash
# Regenerates deepcopy, conversion and OpenAPI code for the node.kuberoot.dev API.
set -euo pipefail
cd "$(dirname "$0")/.."

OPENAPI=k8s.io/kube-openapi/cmd/openapi-gen@v0.0.0-20261001230523-97fa35140926
HEADER=hack/boilerplate.go.txt

go run k8s.io/code-generator/cmd/deepcopy-gen@v0.37.1 --go-header-file $HEADER \
  --output-file zz_generated.deepcopy.go ./pkg/apis/node/...
go run k8s.io/code-generator/cmd/conversion-gen@v0.37.1 --go-header-file $HEADER \
  --output-file zz_generated.conversion.go ./pkg/apis/node/v1alpha1
go run $OPENAPI --go-header-file $HEADER \
  --output-dir pkg/generated/openapi --output-pkg github.com/tym83/kuberoot/pkg/generated/openapi \
  --output-file zz_generated.openapi.go \
  k8s.io/apimachinery/pkg/apis/meta/v1 k8s.io/apimachinery/pkg/runtime k8s.io/apimachinery/pkg/version \
  ./pkg/apis/node/v1alpha1

# The router API: deepcopy, and its CRDs as the router distribution's add-ons.
go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.22.0 object:headerFile=$HEADER \
  paths=./pkg/apis/router/... crd:crdVersions=v1 output:crd:dir=distros/router/addons

# The virtual machines API: deepcopy, and its CRD as the hypervisor distribution's add-on.
go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.22.0 object:headerFile=$HEADER \
  paths=./pkg/apis/vm/... crd:crdVersions=v1 output:crd:dir=distros/hypervisor/addons

# The workstation distribution runs machines too, and workspaces in them.
go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.22.0 \
  paths=./pkg/apis/vm/... crd:crdVersions=v1 output:crd:dir=distros/workstation/addons
go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.22.0 object:headerFile=$HEADER \
  paths=./pkg/apis/workstation/... crd:crdVersions=v1 output:crd:dir=distros/workstation/addons

# The devices API: deepcopy, and its CRDs as add-ons of the iot and the
# observability distributions, which both run kuberoot-devices.
go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.22.0 object:headerFile=$HEADER \
  paths=./pkg/apis/devices/... crd:crdVersions=v1 output:crd:dir=distros/iot/addons
mkdir -p distros/observability/addons
cp distros/iot/addons/devices.kuberoot.dev_*.yaml distros/observability/addons/

# The models API: deepcopy, and its CRD as the ai distribution's add-on.
go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.22.0 object:headerFile=$HEADER \
  paths=./pkg/apis/ai/... crd:crdVersions=v1 output:crd:dir=distros/ai/addons
