package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the controller's cache against an independently mutable kernel
// stand-in, including failures to observe the installed table.
func TestRulesetRepairsIndividualRuleDrift(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FIELD_NFT_TEST_DIR", dir)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := `#!/bin/sh
set -eu
case "$*" in
  '-f -')
    cat > "$FIELD_NFT_TEST_DIR/kernel"
    printf 'applied\n' >> "$FIELD_NFT_TEST_DIR/applies"
    ;;
  '--stateless --numeric list table inet kuberoot_router')
    test ! -e "$FIELD_NFT_TEST_DIR/read-fails" || exit 1
    cat "$FIELD_NFT_TEST_DIR/kernel"
    ;;
  *) exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "nft"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	write := func(name, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	count := func() int {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, "applies"))
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(data), "applied\n")
	}
	want := "table inet kuberoot_router {\n chain forward { policy drop; udp dport 9000 accept; }\n}\n"
	c := &Controller{}
	for i := 0; i < 2; i++ {
		if err := c.applyRuleset(want); err != nil {
			t.Fatal(err)
		}
	}
	if got := count(); got != 1 {
		t.Fatalf("unchanged table applied %d times", got)
	}
	// The table still exists; only an individual rule was deleted.
	write("kernel", "table inet kuberoot_router {\n chain forward { policy drop; }\n}\n")
	if err := c.applyRuleset(want); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "kernel"))
	if err != nil || string(got) != want || count() != 2 {
		t.Fatalf("rule drift not repaired: %q, %v", got, err)
	}
	write("read-fails", "")
	if err := c.applyRuleset(want); err == nil || c.rulesetObserved {
		t.Fatal("an unobservable table must not be cached as verified")
	}
	if err := os.Remove(filepath.Join(dir, "read-fails")); err != nil {
		t.Fatal(err)
	}
	if err := c.applyRuleset(want); err != nil || count() != 4 {
		t.Fatalf("did not retry after observation failure: %v", err)
	}
}
