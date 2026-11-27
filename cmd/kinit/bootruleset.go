package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// nftBinary loads rulesets; a variable for the tests.
var nftBinary = "/usr/sbin/nft"

// loadBootRuleset loads the distribution's boot ruleset, if it names one and
// the file exists. It runs before the network is configured: an address from
// the kernel command line and the API servers that listen on it would
// otherwise be reachable from every link until the router's controller loads
// its own ruleset. A node that never had one, such as a fresh install, keeps
// booting open, so its first configuration can reach it.
func loadBootRuleset(path string) error {
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if out, err := exec.Command(nftBinary, "-f", path).CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %v: %s", path, err, bytes.TrimSpace(out))
	}
	return nil
}
