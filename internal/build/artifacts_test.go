package build

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/jourloy/kilnbridge/internal/model"
)

func writeResult(t *testing.T, work, name, content string) {
	t.Helper()
	file := filepath.Join(work, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestSelectedArtifactsRetainBytesPathsHashesAndUploadRetries(t *testing.T) {
	work, job := t.TempDir(), t.TempDir()
	expected := map[string]string{
		"dist/setup.msi": "installer", "dist/app.tar.gz": "archive", "bin/cli": "executable",
		"lib/core.dll": "library", "lib/nested/core.pdb": "symbols", "reports/junit.xml": "<testsuite/>",
		"reports/manifest.json": "{\"report\":true}", "reports/empty.log": "",
	}
	for name, content := range expected {
		writeResult(t, work, name, content)
	}
	writeResult(t, work, "dist/unselected.exe", "not selected")
	writeResult(t, work, "source.txt", "source")
	s := model.Spec{Recipe: "custom", Project: "demo", Version: "1.2.3", ArtifactPaths: []string{"dist/setup.msi", "dist/app.tar.gz", "bin/cli", "lib", "reports"}, Storage: model.Storage{Prefix: "releases"}}
	artifacts, err := collectArtifacts(context.Background(), job, work, "id", s)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != len(expected) {
		t.Fatal(artifacts)
	}
	out := filepath.Join(job, "artifacts")
	var names []string
	for _, a := range artifacts {
		content, ok := expected[a.Name]
		if !ok {
			t.Fatalf("unexpected artifact: %s", a.Name)
		}
		b, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(a.Name)))
		if err != nil || string(b) != content {
			t.Fatalf("changed artifact %s: %v", a.Name, err)
		}
		digest, _ := FileSHA256(filepath.Join(out, filepath.FromSlash(a.Name)))
		if digest != a.SHA256 {
			t.Fatal("incorrect digest")
		}
		names = append(names, a.Name)
	}
	if !sort.StringsAreSorted(names) {
		t.Fatal("manifest ordering is unstable")
	}
	var manifest struct {
		Artifacts []model.Artifact `json:"artifacts"`
	}
	b, _ := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err := json.Unmarshal(b, &manifest); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(manifest.Artifacts, artifacts) {
		t.Fatal("manifest lost file metadata")
	}
	sums, _ := os.ReadFile(filepath.Join(out, "SHA256SUMS.txt"))
	for _, a := range artifacts {
		if !strings.Contains(string(sums), a.SHA256+"  "+a.Name+"\n") {
			t.Fatal("missing nested checksum")
		}
	}
	// Recreating metadata must not include metadata itself, but must retain a
	// report's own nested manifest.json.
	again, err := Manifest(out, "id", s)
	if err != nil || !reflect.DeepEqual(again, artifacts) {
		t.Fatal("non-idempotent manifest", err)
	}
	if err := os.RemoveAll(work); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		var calls []string
		err := uploadFiles(context.Background(), out, "id", s, artifacts, nil, func(key, local, contentType string) error {
			name := strings.TrimPrefix(key, "releases/id/")
			calls = append(calls, name)
			if name == "reports/junit.xml" && !strings.Contains(contentType, "xml") {
				t.Fatal(contentType)
			}
			if name == "bin/cli" && contentType != "application/octet-stream" {
				t.Fatal(contentType)
			}
			if _, err := os.Stat(local); err != nil {
				t.Fatal(err)
			}
			if attempt == 0 && len(calls) == 2 {
				return errors.New("temporary storage failure")
			}
			return nil
		})
		if attempt == 0 {
			if err == nil || len(calls) != 2 {
				t.Fatal("upload did not stop")
			}
		} else if err != nil || !reflect.DeepEqual(calls, append(append([]string{}, names...), "SHA256SUMS.txt", "manifest.json")) {
			t.Fatal(calls, err)
		}
	}
	writeResult(t, out, "reports/junit.xml", "modified")
	calls := 0
	err = uploadFiles(context.Background(), out, "id", s, artifacts, nil, func(string, string, string) error { calls++; return nil })
	if err == nil || calls != 0 {
		t.Fatal("modified nested artifact was uploaded")
	}
}

func TestArtifactCollectionRejectsMissingEmptyLinkedAndCancelledResults(t *testing.T) {
	for _, kind := range []string{"missing", "empty", "file-link", "parent-link", "nested-link", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			work, job := t.TempDir(), t.TempDir()
			selected := "result"
			if kind == "empty" || kind == "nested-link" {
				if err := os.Mkdir(filepath.Join(work, selected), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if strings.Contains(kind, "link") {
				outside := t.TempDir()
				writeResult(t, outside, "private.txt", "private")
				target, link := outside, filepath.Join(work, selected)
				if kind == "file-link" {
					target = filepath.Join(outside, "private.txt")
				}
				if kind == "parent-link" {
					selected += "/private.txt"
				}
				if kind == "nested-link" {
					link = filepath.Join(link, "link")
				}
				if err := os.Symlink(target, link); err != nil {
					t.Skip("symlinks unavailable:", err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "cancelled" {
				writeResult(t, work, selected, "binary")
				cancel()
			}
			_, err := collectArtifacts(ctx, job, work, "id", model.Spec{ArtifactPaths: []string{selected}})
			if err == nil {
				t.Fatal("invalid artifacts accepted")
			}
			if kind == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(job, "artifacts", "manifest.json")); !os.IsNotExist(err) {
				t.Fatal("failed collection published metadata")
			}
		})
	}
}

func TestCustomRecipePublishesExplicitFileWithoutOutputDir(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	job := t.TempDir()
	archive := makeZIP(t, "app/source.txt")
	b, _ := os.ReadFile(archive)
	if err := os.WriteFile(filepath.Join(job, "source.zip"), b, 0600); err != nil {
		t.Fatal(err)
	}
	digest, _ := FileSHA256(archive)
	command := func(action string) model.Command {
		return model.Command{Run: executable, Args: []string{"-test.run=^TestRecipeHelper$", "kilnbridge-helper", action}}
	}
	s := model.Spec{Recipe: "custom", WorkDir: "app", Project: "demo", Version: "1.2.3", ArchiveSHA256: digest, TestCommands: []model.Command{command("test")}, Commands: []model.Command{command("build")}, ArtifactPaths: []string{"release/application.msi"}}
	a, err := (Runner{Tools: filepath.Join(job, "tools")}).Build(context.Background(), job, "id", s)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 1 || a[0].Name != "release/application.msi" {
		t.Fatal("explicit artifact replaced by ZIP", a)
	}
}
