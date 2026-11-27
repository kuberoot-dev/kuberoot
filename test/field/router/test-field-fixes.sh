#!/bin/bash
set -euo pipefail
export PATH=/work/toolchain/go/bin:$PATH TMPDIR=/work/build-main/tmp
export GOCACHE=/work/build-main/gocache GOPATH=/work/build-main/gopath GOMAXPROCS=2 GOFLAGS='-p=2 -buildvcs=false'
cd /work/field-fixes
gofmt -w pkg/nodeapi/audit*.go pkg/nodeapi/forwarding*.go pkg/nodeapi/upgrade.go pkg/nodeapi/server.go pkg/router/peer_policy_test.go pkg/router/config.go pkg/router/daemons.go pkg/router/fixtures_test.go pkg/apis/router/v1alpha1/types.go
go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.22.0 object:headerFile=hack/boilerplate.go.txt paths=./pkg/apis/router/... crd:crdVersions=v1 output:crd:dir=distros/router/addons
mkdir -p /work/field/fixtures
ROUTER_FIXTURES=/work/field/fixtures go test ./pkg/router ./pkg/safeguard ./pkg/upgrade ./pkg/nodeapi
