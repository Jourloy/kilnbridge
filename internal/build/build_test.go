package build

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jourloy/kilnbridge/internal/model"
)

func makeZIP(t *testing.T, names ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "source.zip")
	f, e := os.Create(p)
	if e != nil {
		t.Fatal(e)
	}
	z := zip.NewWriter(f)
	for _, n := range names {
		w, e := z.Create(n)
		if e != nil {
			t.Fatal(e)
		}
		_, _ = w.Write([]byte("source"))
	}
	if e = z.Close(); e != nil {
		t.Fatal(e)
	}
	_ = f.Close()
	return p
}
func TestExtractRejectsWindowsTraversalAndAliases(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "C:/escape", "a\\b", "file:stream", "CON.txt", "nul", "COM1", "COM¹.txt", "LPT²", "dir./file", "dir /file", "bad\x01.txt"} {
		t.Run(name, func(t *testing.T) {
			p := makeZIP(t, name)
			if e := Extract(p, filepath.Join(t.TempDir(), "out")); e == nil {
				t.Fatalf("accepted %q", name)
			}
		})
	}
	if e := Extract(makeZIP(t, "File.txt", "file.txt"), filepath.Join(t.TempDir(), "out")); e == nil {
		t.Fatal("accepted case-insensitive duplicate")
	}
}
func TestExtractFreshDirectoryAndNestedFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	if e := Extract(makeZIP(t, "mobile/pubspec.yaml", "mobile/lib/main.dart"), dir); e != nil {
		t.Fatal(e)
	}
	if e := Extract(makeZIP(t, "extra"), dir); e == nil {
		t.Fatal("reused a build directory")
	}
	if b, e := os.ReadFile(filepath.Join(dir, "mobile/lib/main.dart")); e != nil || string(b) != "source" {
		t.Fatal("lost file")
	}
}
func TestExtractRejectsSymlinkAndExpansion(t *testing.T) {
	for _, link := range []bool{true, false} {
		p := filepath.Join(t.TempDir(), "source.zip")
		f, _ := os.Create(p)
		z := zip.NewWriter(f)
		h := &zip.FileHeader{Name: "escape", Method: zip.Store}
		if link {
			h.SetMode(os.ModeSymlink | 0777)
		} else {
			h.UncompressedSize64 = MaxExtractedBytes + 1
		}
		w, _ := z.CreateRaw(h)
		_, _ = w.Write([]byte("x"))
		_ = z.Close()
		_ = f.Close()
		if e := Extract(p, filepath.Join(t.TempDir(), "out")); e == nil {
			t.Fatal("accepted unsafe archive")
		}
	}
}
func fixture(t *testing.T) (string, model.Spec, []model.Artifact) {
	t.Helper()
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "demo-1.2.3-windows-x64.zip"), []byte("zip contents"), 0600)
	_ = os.WriteFile(filepath.Join(dir, "demo-1.2.3-windows-x64-setup.exe"), []byte("installer"), 0600)
	s := model.Spec{Project: "demo", Version: "1.2.3", BuildNumber: "4", SourceSHA: strings.Repeat("a", 40), ArchiveSHA256: strings.Repeat("b", 64), SourceDirty: true, Storage: model.Storage{Prefix: "release-candidates"}}
	a, e := Manifest(dir, "build-id", s)
	if e != nil {
		t.Fatal(e)
	}
	return dir, s, a
}
func TestManifestMatchesPlatformContractAndRecordsSkippedTests(t *testing.T) {
	dir, s, _ := fixture(t)
	s.SkipTests = true
	if _, e := Manifest(dir, "build-id", s); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	var m map[string]any
	if e := json.Unmarshal(b, &m); e != nil {
		t.Fatal(e)
	}
	if m["platform"] != "windows" || m["testStatus"] != "skipped" || m["sourceDirty"] != true {
		t.Fatalf("unexpected manifest: %s", b)
	}
	p := m["platforms"].(map[string]any)
	if p["android"] != "skipped" || p["windows"] != "success" {
		t.Fatal(p)
	}
	if len(m["artifacts"].([]any)) != 2 {
		t.Fatal("metadata files were hashed as artifacts")
	}
}
func TestUploadPublishesManifestLastAndStopsOnFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		dir, s, artifacts := fixture(t)
		var calls []string
		e := uploadFiles(context.Background(), dir, "id", s, artifacts, nil, func(key, local, ct string) error {
			calls = append(calls, filepath.Base(key))
			if fail && len(calls) == 2 {
				return errors.New("storage unavailable")
			}
			return nil
		})
		if fail {
			if e == nil || strings.Contains(strings.Join(calls, " "), "manifest.json") {
				t.Fatal("published an incomplete build")
			}
		} else if e != nil || calls[len(calls)-1] != "manifest.json" || len(calls) != 4 {
			t.Fatalf("wrong upload sequence %v %v", calls, e)
		}
	}
}
func TestUploadRejectsModifiedArtifactBeforeAnyNetworkRequest(t *testing.T) {
	dir, s, a := fixture(t)
	_ = os.WriteFile(filepath.Join(dir, a[0].Name), []byte("modified"), 0600)
	calls := 0
	e := uploadFiles(context.Background(), dir, "id", s, a, nil, func(string, string, string) error { calls++; return nil })
	if e == nil || calls != 0 {
		t.Fatal("uploaded modified artifact")
	}
}
func TestCommandFailureAndCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if e := RunCommand(ctx, exec.CommandContext(ctx, "sh", "-c", "echo failure; exit 7"), nil); e == nil {
		t.Fatal("lost process exit code")
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	start := time.Now()
	if e := RunCommand(ctx2, exec.CommandContext(ctx2, "sh", "-c", "sleep 20 & wait"), nil); e == nil {
		t.Fatal("ignored cancellation")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("child process survived cancellation")
	}
}
