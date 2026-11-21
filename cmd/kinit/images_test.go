package main

import (
	"strings"
	"testing"
)

func TestWithSandboxImage(t *testing.T) {
	if got := withSandboxImage(containerdConfig, ""); got != containerdConfig {
		t.Fatalf("without a bundled pause image the configuration changed:\n%s", got)
	}
	cfg := withSandboxImage(containerdConfig, "localhost/kuberoot/pause:1")
	want := "[plugins.'io.containerd.cri.v1.images'.pinned_images]\n  sandbox = \"localhost/kuberoot/pause:1\"\n"
	if !strings.HasSuffix(cfg, want) {
		t.Fatalf("no pinned sandbox image in:\n%s", cfg)
	}
	if strings.Count(cfg, "[plugins.'io.containerd.cri.v1.images'") != 1 {
		t.Fatalf("the images plugin is configured twice:\n%s", cfg)
	}
}
