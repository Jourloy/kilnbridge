package build

import (
	"archive/zip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jourloy/kilnbridge/internal/model"
)

// Run the test executable as a portable stand-in for a project's own build tool.
func TestRecipeHelper(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "kilnbridge-helper" {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "test":
		if err := os.WriteFile("tested", []byte("ok"), 0600); err != nil {
			t.Fatal(err)
		}
	case "build":
		if _, err := os.Stat("tested"); err != nil {
			t.Fatal("tests did not run before the build")
		}
		if err := os.Mkdir("release", 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile("release/application.msi", []byte("installer"), 0600); err != nil {
			t.Fatal(err)
		}
	case "fail":
		os.Exit(7)
	}
}

func TestCustomRecipeBuildsWithoutFlutterAndStopsOnFailure(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := func(action string) model.Command {
		return model.Command{Run: executable, Args: []string{"-test.run=^TestRecipeHelper$", "kilnbridge-helper", action}}
	}
	for _, fail := range []bool{false, true} {
		job := t.TempDir()
		archive := makeZIP(t, "app/source.txt")
		b, _ := os.ReadFile(archive)
		if err := os.WriteFile(filepath.Join(job, "source.zip"), b, 0600); err != nil {
			t.Fatal(err)
		}
		digest, _ := FileSHA256(archive)
		testCommand := command("test")
		if fail {
			testCommand = command("fail")
		}
		s := model.Spec{SourceType: "zip", Recipe: "custom", Project: "demo", Version: "1.2.3", BuildNumber: "1", WorkDir: "app", ArchiveSHA256: digest, OutputDir: "release", TestCommands: []model.Command{testCommand}, Commands: []model.Command{command("build")}}
		if err := s.Validate(); err != nil {
			t.Fatal(err)
		}
		artifacts, err := (Runner{Tools: filepath.Join(job, "tools")}).Build(context.Background(), job, "job-id", s)
		if fail {
			if err == nil {
				t.Fatal("test failure was ignored")
			}
			if _, err := os.Stat(filepath.Join(job, "artifacts")); !os.IsNotExist(err) {
				t.Fatal("failed job was packaged")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(artifacts) != 1 {
			t.Fatal(artifacts)
		}
		z, err := zip.OpenReader(filepath.Join(job, "artifacts", artifacts[0].Name))
		if err != nil {
			t.Fatal(err)
		}
		if len(z.File) != 1 || z.File[0].Name != "application.msi" {
			t.Fatal("output directory was not packaged")
		}
		_ = z.Close()
		manifest, _ := os.ReadFile(filepath.Join(job, "artifacts", "manifest.json"))
		var m map[string]any
		if err := json.Unmarshal(manifest, &m); err != nil {
			t.Fatal(err)
		}
		if m["recipe"] != "custom" || m["testStatus"] != "success" {
			t.Fatal(string(manifest))
		}
	}
}

func TestRecipePackagingRejectsMissingEmptyAndLinkedOutputs(t *testing.T) {
	for _, kind := range []string{"missing", "empty", "link", "nested-link"} {
		t.Run(kind, func(t *testing.T) {
			work := t.TempDir()
			output := "release"
			if kind == "empty" || kind == "nested-link" {
				if err := os.Mkdir(filepath.Join(work, output), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if strings.Contains(kind, "link") {
				outside := t.TempDir()
				if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("private"), 0600); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(work, output)
				if kind == "nested-link" {
					link = filepath.Join(link, "linked")
				}
				if err := os.Symlink(outside, link); err != nil {
					t.Skip("symlinks unavailable:", err)
				}
			}
			_, err := packageRecipe(t.TempDir(), work, "id", model.Spec{OutputDir: output}, func(string) {})
			if err == nil {
				t.Fatal("unsafe or empty output was accepted")
			}
		})
	}
}
