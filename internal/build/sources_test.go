package build

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPreparedArchiveValidationMatchesExtractionRules(t *testing.T) {
	for _, names := range [][]string{{"../escape"}, {"CON.txt"}, {"File", "file"}, {"dir", "dir/file"}, {"Dir/file", "dir"}} {
		if err := ValidateArchive(makeZIP(t, names...)); err == nil {
			t.Fatalf("accepted %v", names)
		}
	}
	if err := ValidateArchive(makeZIP(t, "project/script.ps1")); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "corrupt.zip")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	w, _ := z.CreateHeader(&zip.FileHeader{Name: "script.ps1", Method: zip.Store})
	_, _ = w.Write([]byte("unique script bytes"))
	_ = z.Close()
	_ = f.Close()
	b, _ := os.ReadFile(file)
	b[bytes.Index(b, []byte("unique script bytes"))] ^= 1
	if err = os.WriteFile(file, b, 0600); err != nil {
		t.Fatal(err)
	}
	if err = ValidateArchive(file); err == nil {
		t.Fatal("accepted corrupt CRC")
	}
}

func TestNonGitManifestRecordsArchiveInsteadOfCommit(t *testing.T) {
	for _, kind := range []string{"folder", "zip"} {
		dir, s, _ := fixture(t)
		s.SourceType, s.SourceSHA, s.SourceDirty = kind, "", false
		if _, err := Manifest(dir, "id", s); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		if m["sourceType"] != kind || m["sourceArchiveSha256"] != s.ArchiveSHA256 {
			t.Fatal(string(b))
		}
		if _, ok := m["sourceSha"]; ok {
			t.Fatal("invented commit")
		}
		if _, ok := m["sourceDirty"]; ok {
			t.Fatal("invented clean checkout")
		}
	}
}
