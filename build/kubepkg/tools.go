//go:build tools

// Package tools records the kubepkg commands this module builds.
package tools

import (
	_ "github.com/kuberoot-dev/kubepkg/pkg/cli"
	_ "github.com/kuberoot-dev/kubepkg/pkg/operator"
)
