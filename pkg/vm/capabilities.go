package vm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
)

const monitorExecutable = "/usr/bin/cloud-hypervisor"
const armCPUHotaddCapability = "arm-gicv2-acpi-cpu-hotadd-v1"
const maxCapabilityOutput = 16 << 10

var errCPUHotplugUnavailable = errors.New("ARM CPU hot-add is unavailable")

type hostCapabilities struct {
	SchemaVersion      int      `json:"schema_version"`
	Architecture       string   `json:"architecture"`
	Backend            string   `json:"backend"`
	PreferredGIC       *int     `json:"preferred_gic"`
	Capabilities       []string `json:"capabilities"`
	MaximumHotaddVCPUs int32    `json:"maximum_hotadd_vcpus"`
}

// A launch retains the executable it probed. An atomic replacement of the
// installed path between preflight and restart must not select another binary.
// The image filesystem is read-only; in-place writes to that inode are excluded.
type launchPlan struct {
	executable    *os.File
	armCPUHotplug bool
}

func (p *launchPlan) close() {
	if p != nil && p.executable != nil {
		_ = p.executable.Close()
	}
}

func (m *Machines) architecture() string {
	if m.arch != "" {
		return m.arch
	}
	return runtime.GOARCH
}

// prepareLaunch is called before either restart path stops a running guest.
// Running guests use their own vm.info mode, never a probe of a newer binary.
func (m *Machines) prepareLaunch(ctx context.Context, s node.MachineSpec) (*launchPlan, error) {
	p := &launchPlan{}
	if m.architecture() != "arm64" || s.MaxCPUs == 0 {
		return p, nil
	}
	if err := validateCeilings(s); err != nil {
		return nil, err
	}
	var raw []byte
	var err error
	if m.probeCapabilities != nil {
		raw, err = m.probeCapabilities(ctx)
	} else {
		p.executable, err = os.Open(monitorExecutable)
		if err == nil {
			var info os.FileInfo
			info, err = p.executable.Stat()
			if err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0) {
				err = errors.New("installed VMM is not an executable regular file")
			}
		}
		if err == nil {
			raw, err = probeHostCapabilities(ctx, p.executable)
		}
	}
	if err == nil {
		err = validateARMCPUCapability(raw, s.MaxCPUs)
	}
	if err != nil {
		p.close()
		return nil, fmt.Errorf("%w: %w", errCPUHotplugUnavailable, err)
	}
	p.armCPUHotplug = true
	return p, nil
}

func validateARMCPUCapability(raw []byte, ceiling int32) error {
	if len(raw) > maxCapabilityOutput {
		return errors.New("host capability response exceeds the size limit")
	}
	var caps hostCapabilities
	if err := json.Unmarshal(raw, &caps); err != nil {
		return fmt.Errorf("decode host capabilities: %w", err)
	}
	if caps.SchemaVersion != 1 || caps.Architecture != "aarch64" || caps.Backend != "kvm" ||
		caps.PreferredGIC == nil || *caps.PreferredGIC != 2 ||
		!slices.Contains(caps.Capabilities, armCPUHotaddCapability) ||
		caps.MaximumHotaddVCPUs != 8 {
		return errors.New("installed VMM and host do not report the verified GICv2 CPU hot-add contract")
	}
	if ceiling <= 0 || ceiling > caps.MaximumHotaddVCPUs {
		return fmt.Errorf("maxCPUs must be between 1 and %d on this ARM host", caps.MaximumHotaddVCPUs)
	}
	return nil
}

// ExtraFiles maps the retained executable to descriptor 3 in the child before
// exec. Keep argv[0] stable for process inventory and operational diagnostics.
func monitorCommand(ctx context.Context, executable *os.File, args ...string) *exec.Cmd {
	path := monitorExecutable
	if executable != nil {
		path = "/proc/self/fd/3"
	}
	var cmd *exec.Cmd
	if ctx == nil {
		cmd = exec.Command(path, args...)
	} else {
		cmd = exec.CommandContext(ctx, path, args...)
	}
	cmd.Args[0] = monitorExecutable
	if executable != nil {
		cmd.ExtraFiles = []*os.File{executable}
	}
	return cmd
}

type capabilityOutput struct{ buffer bytes.Buffer }

func (b *capabilityOutput) Write(p []byte) (int, error) {
	if len(p) > maxCapabilityOutput-b.buffer.Len() {
		return 0, io.ErrShortBuffer
	}
	return b.buffer.Write(p)
}

func probeHostCapabilities(ctx context.Context, executable *os.File) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := monitorCommand(ctx, executable, "--host-capabilities")
	cmd.WaitDelay = time.Second
	var stdout, stderr capabilityOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("host capability probe: %w: %s", err, strings.TrimSpace(stderr.buffer.String()))
	}
	return stdout.buffer.Bytes(), nil
}
