package client

import (
	"archive/zip"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func writeSource(t *testing.T, dir, name, data string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0700); err != nil {
		t.Fatal(err)
	}
}

func sourceNames(t *testing.T, file string) []string {
	t.Helper()
	z, err := zip.OpenReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	var names []string
	for _, f := range z.File {
		names = append(names, f.Name)
	}
	return names
}

func TestFolderSourceWithoutGitFiltersFilesAndKeepsScripts(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // No Git installation required.
	dir := t.TempDir()
	for _, name := range []string{"app/pubspec.yaml", "app/scripts/build.ps1", ".git/config", ".env", "node_modules/library.js", "build/cache", "build/fixtures/key.pem", "ignored.txt", ".gitignore"} {
		writeSource(t, dir, name, "source")
	}
	writeSource(t, dir, ".gitignore", "ignored.txt\n")
	if err := os.Mkdir(filepath.Join(dir, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "source.zip")
	writeSource(t, dir, "source.zip", "previous ZIP")
	writeSource(t, dir, "source.zip.json", "previous metadata")
	info, err := PrepareSource("folder", dir, output, "build/fixtures/key.pem")
	if err != nil {
		t.Fatal(err)
	}
	if info.Type != "folder" || info.SHA != "" || info.Dirty || len(info.Digest) != 64 {
		t.Fatal(info)
	}
	want := []string{".gitignore", "app/", "app/pubspec.yaml", "app/scripts/", "app/scripts/build.ps1", "build/fixtures/key.pem", "empty/", "ignored.txt"}
	if names := sourceNames(t, output); !reflect.DeepEqual(names, want) {
		t.Fatalf("unexpected files: %v", names)
	}
	b, err := SourceFile(output, "app/scripts/build.ps1")
	if err != nil || string(b) != "source" {
		t.Fatal("lost script", err)
	}
}

func TestPreparedZIPPreservesExactBytesIncludingProducerSelectedFiles(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	writeSource(t, dir, "app/pubspec.yaml", "version: 2.3.4+56\n")
	writeSource(t, dir, ".env", "synthetic fixture explicitly selected by ZIP producer")
	input := filepath.Join(t.TempDir(), "prepared.zip")
	if _, err := PrepareSource("folder", dir, input, ".env"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(input)
	output := filepath.Join(t.TempDir(), "frozen.zip")
	info, err := PrepareSource("zip", input, output)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(output)
	if !bytes.Equal(before, after) || info.Type != "zip" || info.SHA != "" || info.Dirty {
		t.Fatal("ZIP was repackaged or provenance invented")
	}
	b, err := SourceFile(output, "app/pubspec.yaml")
	if err != nil || string(b) != "version: 2.3.4+56\n" {
		t.Fatal("metadata inference failed", err)
	}
	if _, err := SourceFile(output, "pubspec.yaml"); err == nil {
		t.Fatal("lost archive root boundary")
	}
	sidecarOutput := filepath.Join(t.TempDir(), "source.zip")
	if err := os.WriteFile(sidecarOutput+".json", before, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareSource("zip", sidecarOutput+".json", sidecarOutput); err == nil {
		t.Fatal("input ZIP could be overwritten by snapshot metadata")
	}
	if _, err := PrepareSource("zip", input, input); err == nil {
		t.Fatal("overwrote input ZIP")
	}
	if _, err := PrepareSource("zip", input, output, ".env"); err == nil {
		t.Fatal("silently ignored ZIP includes")
	}
	actual, _ := os.ReadFile(input)
	if !bytes.Equal(actual, before) {
		t.Fatal("changed producer ZIP")
	}
}

func TestSourceFailuresPreserveExistingOutput(t *testing.T) {
	for _, scenario := range []string{"empty", "missing-include", "include-directory", "link", "linked-include-parent", "invalid-name", "invalid-type", "not-zip", "duplicate-zip"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			input, kind := dir, "folder"
			var includes []string
			switch scenario {
			case "missing-include":
				includes = []string{"absent.ps1"}
			case "include-directory":
				if err := os.Mkdir(filepath.Join(dir, "scripts"), 0700); err != nil {
					t.Fatal(err)
				}
				includes = []string{"scripts"}
			case "link", "linked-include-parent":
				outside := t.TempDir()
				writeSource(t, outside, "secret", "private")
				if err := os.Symlink(outside, filepath.Join(dir, "linked")); err != nil {
					t.Skip(err)
				}
				if scenario == "linked-include-parent" {
					includes = []string{"linked/secret"}
				}
			case "invalid-name":
				writeSource(t, dir, "CON.txt", "bad")
			case "invalid-type":
				kind = "remote"
			case "not-zip":
				writeSource(t, dir, "source.zip", "not a ZIP")
				input, kind = filepath.Join(dir, "source.zip"), "zip"
			case "duplicate-zip":
				input, kind = filepath.Join(dir, "source.zip"), "zip"
				f, err := os.Create(input)
				if err != nil {
					t.Fatal(err)
				}
				z := zip.NewWriter(f)
				for _, name := range []string{"scripts/build.ps1", "SCRIPTS/BUILD.PS1"} {
					w, _ := z.Create(name)
					_, _ = w.Write([]byte("script"))
				}
				if err = z.Close(); err != nil {
					t.Fatal(err)
				}
				_ = f.Close()
			}
			output := filepath.Join(t.TempDir(), "existing.zip")
			if err := os.WriteFile(output, []byte("keep me"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := PrepareSource(kind, input, output, includes...); err == nil {
				t.Fatal("accepted invalid source")
			}
			b, _ := os.ReadFile(output)
			if string(b) != "keep me" {
				t.Fatal("destroyed existing output")
			}
			files, _ := os.ReadDir(filepath.Dir(output))
			if len(files) != 1 {
				t.Fatal("left temporary source behind")
			}
		})
	}
}

func TestGitSnapshotIncludesIgnoredScriptAndRecordsDirty(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if b, e := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); e != nil {
			t.Fatalf("git: %s %v", b, e)
		}
	}
	git("init")
	writeSource(t, dir, ".gitignore", "scripts/\n")
	git("add", ".gitignore")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "Initial")
	writeSource(t, dir, "scripts/build.ps1", "script")
	output := filepath.Join(t.TempDir(), "source.zip")
	info, err := PrepareSource("git", dir, output, "scripts/build.ps1")
	if err != nil || !info.Dirty || len(info.SHA) != 40 {
		t.Fatal(info, err)
	}
	if _, err = SourceFile(output, "scripts/build.ps1"); err != nil {
		t.Fatal(err)
	}
}
