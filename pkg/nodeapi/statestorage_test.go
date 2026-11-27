package nodeapi

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseMountInfo(t *testing.T) {
	for _, c := range []struct {
		line string
		want mount
		ok   bool
	}{
		{"36 25 254:17 / /var rw,nosuid,nodev,relatime shared:12 - ext4 /dev/vdb1 rw,errors=remount-ro,nodelalloc", mount{"/dev/vdb1", "ext4", false}, true},
		{"36 25 254:17 / /var ro,nosuid,nodev,relatime shared:12 - ext4 /dev/vdb1 ro,errors=remount-ro", mount{"/dev/vdb1", "ext4", true}, true},
		{"30 25 0:27 / /var rw,nosuid,nodev - tmpfs tmpfs rw,mode=755", mount{"tmpfs", "tmpfs", false}, true},
		{"36 25 254:17 / /var/lib rw - ext4 /dev/vdb1 rw", mount{}, false},
	} {
		got, ok := parseMountInfo(c.line, "/var")
		if ok != c.ok || got != c.want {
			t.Errorf("parseMountInfo(%q) = %+v, %v; want %+v, %v", c.line, got, ok, c.want, c.ok)
		}
	}
}

func TestProbeReportsAReadThatDoesNotFinish(t *testing.T) {
	release := make(chan struct{})
	calls := 0
	p := &probe{read: func(string) error { calls++; <-release; return nil }}
	start := time.Now()
	err := p.check("/dev/vdb1")
	if err == nil || !strings.Contains(err.Error(), "has not finished") {
		t.Fatalf("a stuck read: %v", err)
	}
	if time.Since(start) > probeTimeout+time.Second {
		t.Errorf("the check waited %s", time.Since(start))
	}
	// Further checks do not start another read behind the stuck one.
	if err := p.check("/dev/vdb1"); err == nil || calls != 1 {
		t.Errorf("second check: %v, %d reads", err, calls)
	}
	close(release)
	time.Sleep(50 * time.Millisecond)
	if err := p.check("/dev/vdb1"); err != nil {
		t.Errorf("after the read finished: %v", err)
	}
}

func TestProbeKeepsTheReadError(t *testing.T) {
	eio := errors.New("read /dev/vdb1: input/output error")
	p := &probe{read: func(string) error { return eio }}
	if err := p.check("/dev/vdb1"); !errors.Is(err, eio) {
		t.Fatalf("check = %v", err)
	}
}
