package nodeapi

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
)

func controllerProcesses(proc, service string) ([]node.ControllerProcess, string) {
	entries, err := os.ReadDir(proc)
	if err != nil {
		return nil, "read process inventory: " + err.Error()
	}
	boot, err := os.ReadFile(filepath.Join(proc, "sys/kernel/random/boot_id"))
	if err != nil {
		return nil, "read boot identity: " + err.Error()
	}
	bootID := strings.TrimSpace(string(boot))
	if bootID == "" {
		return nil, "boot identity is empty"
	}
	var processes []node.ControllerProcess
	for _, entry := range entries {
		pid, err := strconv.ParseInt(entry.Name(), 10, 32)
		if err != nil || pid <= 0 {
			continue
		}
		base := filepath.Join(proc, entry.Name())
		executable, exeErr := os.Readlink(filepath.Join(base, "exe"))
		if exeErr != nil && !os.IsNotExist(exeErr) {
			return nil, fmt.Sprintf("inspect executable of PID %d: %v", pid, exeErr)
		}
		comm, commErr := os.ReadFile(filepath.Join(base, "comm"))
		if commErr != nil && !os.IsNotExist(commErr) {
			return nil, fmt.Sprintf("inspect process name of PID %d: %v", pid, commErr)
		}
		clean := strings.TrimSuffix(executable, " (deleted)")
		if filepath.Base(clean) != service && strings.TrimSpace(string(comm)) != service {
			continue
		}
		if exeErr != nil {
			if _, err := os.Stat(base); os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Sprintf("controller PID %d has no readable executable", pid)
		}
		// comm is mutable. A matching name with another executable is an
		// unverified process, not evidence that a known controller is safe.
		if clean != "/usr/bin/"+service || clean != executable {
			return nil, fmt.Sprintf("controller PID %d has an unexpected or replaced executable", pid)
		}
		cmd, err := os.ReadFile(filepath.Join(base, "cmdline"))
		if err != nil {
			return nil, fmt.Sprintf("inspect controller PID %d command identity: %v", pid, err)
		}
		first, _, _ := bytes.Cut(cmd, []byte{0})
		if filepath.Clean(string(first)) != clean {
			return nil, fmt.Sprintf("controller PID %d command identity differs from executable", pid)
		}
		stat, err := os.ReadFile(filepath.Join(base, "stat"))
		if err != nil {
			return nil, fmt.Sprintf("inspect controller PID %d incarnation: %v", pid, err)
		}
		_, tail, ok := strings.Cut(string(stat), ") ")
		fields := strings.Fields(tail)
		if !ok || len(fields) <= 19 {
			return nil, fmt.Sprintf("controller PID %d has malformed process stat", pid)
		}
		start, err := strconv.ParseInt(fields[19], 10, 64)
		if err != nil || start <= 0 {
			return nil, fmt.Sprintf("controller PID %d has invalid start ticks", pid)
		}
		// Re-read after the other files to reject an exec/reuse during the
		// observation instead of publishing a mixed process incarnation.
		again, err := os.ReadFile(filepath.Join(base, "stat"))
		if err != nil {
			return nil, fmt.Sprintf("controller PID %d vanished during inventory", pid)
		}
		_, tail, ok = strings.Cut(string(again), ") ")
		fields = strings.Fields(tail)
		if !ok || len(fields) <= 19 || fields[19] != strconv.FormatInt(start, 10) {
			return nil, fmt.Sprintf("controller PID %d changed incarnation during inventory", pid)
		}
		exeAgain, err := os.Readlink(filepath.Join(base, "exe"))
		if err != nil || exeAgain != executable {
			return nil, fmt.Sprintf("controller PID %d changed executable during inventory", pid)
		}
		processes = append(processes, node.ControllerProcess{PID: int32(pid), Executable: executable, StartTicks: start, BootID: bootID})
	}
	sort.Slice(processes, func(i, j int) bool { return processes[i].PID < processes[j].PID })
	return processes, ""
}
