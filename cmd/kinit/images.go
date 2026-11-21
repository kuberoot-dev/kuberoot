package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// bundledImagesDir holds OCI archives of images the root filesystem carries
// itself, for an architecture upstream publishes no image of: the pause
// image on riscv64. pause.ref names the pause image among them.
const bundledImagesDir = "/usr/share/kuberoot/images"

// bundledSandboxImage is the reference of the pause image the root
// filesystem carries, or empty when containerd should use its own default.
func bundledSandboxImage() string {
	raw, err := os.ReadFile(filepath.Join(bundledImagesDir, "pause.ref"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// withSandboxImage points containerd's pod sandboxes at ref, pinned so the
// kubelet's image garbage collection keeps it; cfg is unchanged without one.
func withSandboxImage(cfg, ref string) string {
	if ref == "" {
		return cfg
	}
	return cfg + fmt.Sprintf("\n[plugins.'io.containerd.cri.v1.images'.pinned_images]\n  sandbox = %q\n", ref)
}

// importBundledImages loads the carried images into the namespace the CRI
// uses, once containerd answers. The first pod sandbox waits for it: until
// then a sandbox fails to find its image and the kubelet tries again.
func importBundledImages(ctx context.Context) {
	archives, _ := filepath.Glob(filepath.Join(bundledImagesDir, "*.tar"))
	for _, a := range archives {
		args := []string{"/usr/bin/ctr", "--address", "/run/containerd/containerd.sock", "--namespace", "k8s.io", "images", "import", a}
		if runUntilSuccess(ctx, args, nil, 2*time.Second) {
			log.Printf("image %s imported", filepath.Base(a))
		}
	}
}
