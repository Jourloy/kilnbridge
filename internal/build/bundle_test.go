package build

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jourloy/kilnbridge/internal/model"
)

func TestBundlePreservesNestedResultsAndIsDeterministic(t *testing.T) {
	out, dir := t.TempDir(), t.TempDir()
	writeResult(t, out, "lib/core.dll", "binary")
	writeResult(t, out, "reports/junit.xml", "<testsuite/>")
	artifacts, err := Manifest(out, "job", model.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	writeResult(t, out, "not-selected.txt", "not in the job")
	first, err := CreateBundle(context.Background(), out, filepath.Join(dir, "first.zip"), artifacts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CreateBundle(context.Background(), out, filepath.Join(dir, "second.zip"), artifacts)
	if err != nil || first.SHA256 != second.SHA256 || first.Size != second.Size {
		t.Fatal("unstable bundle", err)
	}
	z, err := zip.OpenReader(filepath.Join(dir, "first.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	var names []string
	for _, entry := range z.File {
		names = append(names, entry.Name)
		r, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(entry.Name)))
		if err != nil || string(b) != string(want) {
			t.Fatal("changed file", entry.Name, err)
		}
	}
	if !reflect.DeepEqual(names, []string{"lib/core.dll", "reports/junit.xml", "SHA256SUMS.txt", "manifest.json"}) {
		t.Fatal(names)
	}
	writeResult(t, out, "lib/core.dll", "changed")
	if _, err := CreateBundle(context.Background(), out, filepath.Join(dir, "bad.zip"), artifacts); err == nil {
		t.Fatal("bundled changed artifact")
	}
	if _, err := os.Stat(filepath.Join(dir, "bad.zip")); !os.IsNotExist(err) {
		t.Fatal("partial bundle kept")
	}
}
