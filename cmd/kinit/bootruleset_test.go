package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeNft installs a stand-in nft that records its arguments.
func fakeNft(t *testing.T, exit int) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "args")
	script := "#!/bin/sh\necho \"$@\" >>" + log + "\nexit " + string(rune('0'+exit)) + "\n"
	bin := filepath.Join(dir, "nft")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := nftBinary
	nftBinary = bin
	t.Cleanup(func() { nftBinary = old })
	return log
}

func TestBootRulesetIsLoaded(t *testing.T) {
	log := fakeNft(t, 0)
	rs := filepath.Join(t.TempDir(), "boot.nft")
	if err := os.WriteFile(rs, []byte("table inet kuberoot_router {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loadBootRuleset(rs); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(log)
	if err != nil || strings.TrimSpace(string(got)) != "-f "+rs {
		t.Fatalf("nft ran with %q, %v; want -f %s", got, err, rs)
	}
}

func TestNoBootRulesetBootsOpen(t *testing.T) {
	log := fakeNft(t, 0)
	for _, path := range []string{"", filepath.Join(t.TempDir(), "missing.nft")} {
		if err := loadBootRuleset(path); err != nil {
			t.Errorf("%q: %v", path, err)
		}
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Error("nft ran without a boot ruleset; a fresh node must stay reachable for its first configuration")
	}
}

func TestBootRulesetFailureIsReported(t *testing.T) {
	fakeNft(t, 1)
	rs := filepath.Join(t.TempDir(), "boot.nft")
	if err := os.WriteFile(rs, []byte("garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loadBootRuleset(rs); err == nil {
		t.Fatal("a ruleset nft rejected was reported as loaded")
	}
}
