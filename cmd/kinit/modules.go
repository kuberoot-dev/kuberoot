package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/kuberoot-dev/kuberoot/pkg/distro"
)

// loadModulesAndLock loads the profile's modules, which the kernel accepts
// only signed with its own build key, and then disables module loading for
// the rest of the run, whether or not there were any to load.
func loadModulesAndLock(modules []distro.Module) {
	var uts unix.Utsname
	_ = unix.Uname(&uts)
	dir := filepath.Join("/lib/modules", unix.ByteSliceToString(uts.Release[:]), "extra")
	for _, m := range modules {
		if err := loadModule(filepath.Join(dir, m.Name+".ko"), m.Params); err != nil {
			log.Printf("module %s: %v", m.Name, err)
			continue
		}
		log.Printf("module %s loaded", m.Name)
	}
	if err := os.WriteFile("/proc/sys/kernel/modules_disabled", []byte("1"), 0); err != nil {
		log.Printf("disable module loading: %v", err)
	}
}

func loadModule(path, params string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := unix.FinitModule(int(f.Fd()), params, 0); err != nil && err != unix.EEXIST {
		return fmt.Errorf("finit_module: %w", err)
	}
	return nil
}
