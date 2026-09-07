package client

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCertificatePinVerifiedBeforeCredentials(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing authorization")
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer s.Close()
	digest := sha256.Sum256(s.Certificate().Raw)
	good := hex.EncodeToString(digest[:])
	c, e := New(s.URL, "secret", strings.Repeat("0", 64))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.Request(context.Background(), "GET", "/v1/health", nil, ""); e == nil {
		t.Fatal("accepted untrusted certificate")
	}
	if calls.Load() != 0 {
		t.Fatal("sent credentials before checking identity")
	}
	c, e = New(s.URL, "secret", good)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.Request(context.Background(), "GET", "/v1/health", nil, ""); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 1 {
		t.Fatal("pinned request failed")
	}
}
func TestSnapshotIncludesDirtyFilesButExcludesSecretsAndCaches(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if b, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("git: %s %v", b, e)
		}
	}
	git("init")
	_ = os.WriteFile(filepath.Join(dir, "pubspec.yaml"), []byte("version: 1.0.0+1\n"), 0600)
	git("add", "pubspec.yaml")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "Initial")
	for _, name := range []string{"new.dart", "local_release.env", "signing.p12", ".env", "build/output", "test/fixtures/key.pem"} {
		p := filepath.Join(dir, name)
		_ = os.MkdirAll(filepath.Dir(p), 0700)
		_ = os.WriteFile(p, []byte("local"), 0600)
	}
	out := filepath.Join(t.TempDir(), "source.zip")
	sha, dirty, digest, e := Snapshot(dir, out)
	if e != nil {
		t.Fatal(e)
	}
	if !dirty || len(sha) != 40 || len(digest) != 64 {
		t.Fatal("lost provenance")
	}
	z, e := zip.OpenReader(out)
	if e != nil {
		t.Fatal(e)
	}
	defer z.Close()
	names := map[string]bool{}
	for _, f := range z.File {
		names[f.Name] = true
	}
	if len(names) != 2 || !names["new.dart"] || !names["pubspec.yaml"] {
		t.Fatalf("unexpected snapshot: %v", names)
	}
	z.Close()
	_, _, _, e = Snapshot(dir, out, "test/fixtures/key.pem")
	if e != nil {
		t.Fatal(e)
	}
	included, e := zip.OpenReader(out)
	if e != nil {
		t.Fatal(e)
	}
	defer included.Close()
	found := false
	for _, f := range included.File {
		if f.Name == "test/fixtures/key.pem" {
			found = true
		}
		if f.Name == "local_release.env" {
			t.Fatal("include leaked unrelated dotenv")
		}
	}
	if !found {
		t.Fatal("explicit test fixture was excluded")
	}

}
func TestDotenvDoesNotExecuteShell(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.env")
	_ = os.WriteFile(p, []byte("# literal values\nAPI_URL=\"https://example.com\"\nKEY='$(touch surprise)'\n"), 0600)
	env, e := ReadEnv(p)
	if e != nil {
		t.Fatal(e)
	}
	if env["KEY"] != "$(touch surprise)" || env["API_URL"] != "https://example.com" {
		t.Fatal(env)
	}
}
