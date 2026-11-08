package nodeapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agecrypt "filippo.io/age"
)

func TestBackupConfigDefaultsAndChecks(t *testing.T) {
	c, err := parseBackupConfig(nil, "kr-1")
	if err != nil || c.interval != time.Hour || c.keep != 12 || c.maxBytes != 0 || c.s3 != nil {
		t.Errorf("defaults = %+v, %v", c, err)
	}
	for _, bad := range []map[string][]byte{
		{"interval": []byte("1m")},
		{"keep": []byte("0")},
		{"maxBytes": []byte("0")},
		{"maxBytes": []byte("1Gi")},
		{"s3Endpoint": []byte("s3.example.com")},
		{"s3Endpoint": []byte("https://s3.example.com"), "s3Bucket": []byte("b")},
	} {
		if _, err := parseBackupConfig(bad, "kr-1"); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	c, err = parseBackupConfig(map[string][]byte{"s3Endpoint": []byte("https://s3.example.com"), "s3Bucket": []byte("b"),
		"s3AccessKey": []byte("a"), "s3SecretKey": []byte("s"), "allowUnencrypted": []byte("true")}, "kr-1")
	if err != nil || c.s3.prefix != "kuberoot/kr-1/" || c.s3.Region != "us-east-1" {
		t.Errorf("s3 target = %+v, %v", c.s3, err)
	}
}

func fakeBackups(t *testing.T) *stateBackupStorage {
	t.Helper()
	return &stateBackupStorage{nodeName: "kr-1", dir: t.TempDir(), write: func(w io.Writer, _ string) error {
		_, err := io.WriteString(w, "archive")
		return err
	}}
}

func TestBackupsAreTakenUploadedAndPruned(t *testing.T) {
	var mu sync.Mutex
	puts := map[string]string{}
	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=a/") {
			http.Error(w, "unsigned", http.StatusForbidden)
			return
		}
		mu.Lock()
		puts[r.URL.Path] = string(body)
		mu.Unlock()
	}))
	defer store.Close()
	cfg, err := parseBackupConfig(map[string][]byte{"s3Endpoint": []byte(store.URL), "s3Bucket": []byte("backups"),
		"s3AccessKey": []byte("a"), "s3SecretKey": []byte("s"), "keep": []byte("2"), "allowUnencrypted": []byte("true")}, "kr-1")
	if err != nil {
		t.Fatal(err)
	}
	s := fakeBackups(t)
	if !s.due(cfg.interval) {
		t.Error("no backup yet, but none is due")
	}
	for _, name := range []string{"state-1", "state-2", "state-3"} {
		if _, err := s.take(name, cfg.recipients); err != nil {
			t.Fatal(err)
		}
	}
	if s.due(cfg.interval) {
		t.Error("a backup is due right after one was taken")
	}
	s.upload(context.Background(), cfg)
	s.prune(cfg.keep, 0)
	if got := strings.Join(s.names(), ","); got != "state-2,state-3" {
		t.Errorf("kept %s, want the newest two", got)
	}
	b, err := s.load("state-3")
	if err != nil {
		t.Fatal(err)
	}
	if b.Status.Location != "s3://backups/kuberoot/kr-1/state-3.tar.gz" || b.Status.SizeBytes != 7 || len(b.Status.Sha256) != 64 {
		t.Errorf("status = %+v", b.Status)
	}
	if puts["/backups/kuberoot/kr-1/state-3.tar.gz"] != "archive" {
		t.Errorf("uploads = %v", puts)
	}
}

func TestUploadFailureIsRecordedAndRetried(t *testing.T) {
	fail := true
	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(w, "down", http.StatusServiceUnavailable)
		}
	}))
	defer store.Close()
	cfg, _ := parseBackupConfig(map[string][]byte{"s3Endpoint": []byte(store.URL), "s3Bucket": []byte("b"),
		"s3AccessKey": []byte("a"), "s3SecretKey": []byte("s"), "allowUnencrypted": []byte("true")}, "kr-1")
	s := fakeBackups(t)
	if _, err := s.take("state-1", nil); err != nil {
		t.Fatal(err)
	}
	s.upload(context.Background(), cfg)
	if b, _ := s.load("state-1"); b.Status.Location != "" || !strings.Contains(b.Status.Message, "503") {
		t.Errorf("after a failed upload: %+v", b.Status)
	}
	fail = false
	s.upload(context.Background(), cfg)
	if b, _ := s.load("state-1"); b.Status.Location == "" || b.Status.Message != "" {
		t.Errorf("after the retry: %+v", b.Status)
	}
}

func TestBackupsAreOrderedByWhenTheyWereTaken(t *testing.T) {
	s := fakeBackups(t)
	for _, name := range []string{"zz-first", "aa-second"} {
		if _, err := s.take(name, nil); err != nil {
			t.Fatal(err)
		}
		time.Sleep(1100 * time.Millisecond) // archives are stamped to the second
	}
	s.prune(1, 0)
	if got := strings.Join(s.names(), ","); got != "aa-second" {
		t.Errorf("kept %s, want the newest", got)
	}
}

func TestUploadsNeedEncryption(t *testing.T) {
	s3 := map[string][]byte{"s3Endpoint": []byte("https://s3.example.com"), "s3Bucket": []byte("b"),
		"s3AccessKey": []byte("a"), "s3SecretKey": []byte("s")}
	if _, err := parseBackupConfig(s3, "kr-1"); err == nil || !strings.Contains(err.Error(), "encryptionRecipient") {
		t.Errorf("an upload without encryption was accepted: %v", err)
	}
	s3["encryptionRecipient"] = []byte("age1notakey")
	if _, err := parseBackupConfig(s3, "kr-1"); err == nil {
		t.Error("a malformed recipient was accepted")
	}
}

func TestEncryptedBackupRoundTrip(t *testing.T) {
	id, err := agecrypt.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	puts := map[string][]byte{}
	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		puts[r.URL.Path] = body
		mu.Unlock()
	}))
	defer store.Close()
	cfg, err := parseBackupConfig(map[string][]byte{"s3Endpoint": []byte(store.URL), "s3Bucket": []byte("backups"),
		"s3AccessKey": []byte("a"), "s3SecretKey": []byte("s"), "encryptionRecipient": []byte(id.Recipient().String())}, "kr-1")
	if err != nil {
		t.Fatal(err)
	}
	s := fakeBackups(t)
	if _, err := s.take("old", nil); err != nil { // taken before encryption was set up
		t.Fatal(err)
	}
	if _, err := s.take("sealed", cfg.recipients); err != nil {
		t.Fatal(err)
	}
	s.upload(context.Background(), cfg)
	b, _ := s.load("sealed")
	if !b.Status.Encrypted || b.Status.Location != "s3://backups/kuberoot/kr-1/sealed.tar.gz.age" {
		t.Errorf("status = %+v", b.Status)
	}
	if old, _ := s.load("old"); old.Status.Location != "" {
		t.Error("an archive in the clear left the node once encryption was set up")
	}
	sealed := puts["/backups/kuberoot/kr-1/sealed.tar.gz.age"]
	if len(sealed) == 0 || strings.Contains(string(sealed), "archive") {
		t.Fatalf("uploaded %q, want an encrypted archive", sealed)
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "download")
	if err := os.WriteFile(src, sealed, 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "restore.tar.gz")
	if err := openStateArchive(src, dst, ""); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Errorf("opened without an identity: %v", err)
	}
	other, _ := agecrypt.GenerateX25519Identity()
	if err := openStateArchive(src, dst, other.String()); err == nil {
		t.Error("opened with someone else's identity")
	}
	if err := openStateArchive(src, dst, id.String()); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); string(got) != "archive" {
		t.Errorf("decrypted %q", got)
	}
	// An archive in the clear passes through as it is.
	if err := os.WriteFile(src, []byte("plain"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := openStateArchive(src, dst, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); string(got) != "plain" {
		t.Errorf("plain archive became %q", got)
	}
}

func TestOldBackupsGoWhenTheyOutgrowTheirBudget(t *testing.T) {
	s := fakeBackups(t)
	for _, name := range []string{"state-1", "state-2", "state-3"} {
		if _, err := s.take(name, nil); err != nil {
			t.Fatal(err)
		}
		time.Sleep(1100 * time.Millisecond) // archives are stamped to the second
	}
	// Room for one archive and its status: the two older ones have to go
	// although the count allows all three.
	s.prune(12, s.size("state-3"))
	if got := strings.Join(s.names(), ","); got != "state-3" {
		t.Errorf("kept %s, want the newest alone", got)
	}
}

func TestTheNewestBackupSurvivesAnImpossibleBudget(t *testing.T) {
	s := fakeBackups(t)
	if _, err := s.take("state-1", nil); err != nil {
		t.Fatal(err)
	}
	s.prune(12, 1)
	if got := strings.Join(s.names(), ","); got != "state-1" {
		t.Errorf("kept %q, want the newest kept even over budget", got)
	}
}

func TestTheBudgetDefaultsToAShareOfTheFilesystem(t *testing.T) {
	s := fakeBackups(t)
	got := s.budget(0)
	if got <= 0 {
		t.Fatalf("budget = %d, want a share of the filesystem", got)
	}
	if asked := s.budget(4096); asked != 4096 {
		t.Errorf("budget(4096) = %d, want what the Secret asked for", asked)
	}
}

func TestABudgetedPruneIsStillBoundedByTheCount(t *testing.T) {
	s := fakeBackups(t)
	for _, name := range []string{"state-1", "state-2"} {
		if _, err := s.take(name, nil); err != nil {
			t.Fatal(err)
		}
		time.Sleep(1100 * time.Millisecond)
	}
	s.prune(1, 1<<40)
	if got := strings.Join(s.names(), ","); got != "state-2" {
		t.Errorf("kept %s, want the count honoured under a large budget", got)
	}
}
