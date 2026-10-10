package upgrade

import (
	"archive/tar"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kuberoot-dev/kuberoot/pkg/bootdisk"
	"github.com/kuberoot-dev/kuberoot/pkg/installer"
	"github.com/kuberoot-dev/kuberoot/pkg/release"
)

const downloadDir = "/var/lib/kuberoot/upgrade"

// ReleaseKeys holds the public keys release bundles must be signed with,
// built into the image.
const ReleaseKeys = "/usr/share/kuberoot/release.pub"

// Fetch downloads a release bundle (a tar of vmlinuz.efi, initrd.cpio,
// rootfs.squashfs and VERSION) and its signature, url + ".sig". Nothing is
// unpacked before the signature checks out against the image's release keys.
func Fetch(ctx context.Context, url, sum string, report func(done, total int64)) (bootdisk.Artifacts, error) {
	a := bootdisk.Artifacts{Dir: downloadDir, Arch: archName(), ConsoleArg: installer.CurrentBootArgs()}
	_ = os.RemoveAll(downloadDir)
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		return a, err
	}
	trusted, err := os.ReadFile(ReleaseKeys)
	if err != nil {
		return a, fmt.Errorf("release keys: %w", err)
	}
	bundle := filepath.Join(downloadDir, "bundle.tar")
	if err := Download(ctx, url, bundle, report); err != nil {
		return a, err
	}
	defer os.Remove(bundle)
	signature, err := fetchText(ctx, url+".sig")
	if err != nil {
		return a, fmt.Errorf("bundle signature: %w", err)
	}
	digest, err := release.FileSum(bundle)
	if err != nil {
		return a, err
	}
	if err := release.Verify(digest, signature, trusted); err != nil {
		return a, fmt.Errorf("bundle signature: %w", err)
	}
	if sum != "" && !strings.EqualFold(hex.EncodeToString(digest), sum) {
		return a, fmt.Errorf("bundle checksum mismatch")
	}
	if err := unpack(bundle); err != nil {
		return a, err
	}
	version, _ := os.ReadFile(filepath.Join(downloadDir, "VERSION"))
	a.Version = strings.TrimSpace(string(version))
	return a, nil
}

// client gives up on a stalled server instead of waiting forever.
var client = &http.Client{Timeout: 30 * time.Minute, Transport: &http.Transport{
	ResponseHeaderTimeout: time.Minute, IdleConnTimeout: time.Minute,
}}

// Download fetches url into path, reporting progress.
func Download(ctx context.Context, url, path string, report func(done, total int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, &countingReader{r: resp.Body, total: resp.ContentLength, report: report}); err != nil {
		return err
	}
	return f.Sync()
}

func fetchText(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", url, resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return string(raw), err
}

// unpack extracts the bundle's known files, by base name only.
func unpack(bundle string) error {
	f, err := os.Open(bundle)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read bundle: %w", err)
		}
		name := filepath.Base(h.Name)
		switch name {
		case "vmlinuz.efi", "initrd.cpio", "rootfs.squashfs", "VERSION":
		default:
			continue
		}
		out, err := os.Create(filepath.Join(downloadDir, name))
		if err != nil {
			return err
		}
		_, err = io.Copy(out, tr)
		out.Close()
		if err != nil {
			return err
		}
	}
	for _, f := range []string{"vmlinuz.efi", "initrd.cpio", "rootfs.squashfs", "VERSION"} {
		if _, err := os.Stat(filepath.Join(downloadDir, f)); err != nil {
			return fmt.Errorf("bundle has no %s", f)
		}
	}
	return nil
}

type countingReader struct {
	r      io.Reader
	done   int64
	total  int64
	report func(done, total int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.done += int64(n)
	c.report(c.done, c.total)
	return n, err
}
