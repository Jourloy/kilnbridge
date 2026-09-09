package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/jourloy/kilnbridge/internal/build"
	"github.com/jourloy/kilnbridge/internal/client"
	"github.com/jourloy/kilnbridge/internal/model"
)

func TestCLISubmitsFolderAndZIPWithoutGit(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, kind := range []string{"folder", "zip"} {
		for _, recipe := range []string{"flutter", "custom"} {
			t.Run(kind+"/"+recipe, func(t *testing.T) {
				source := t.TempDir()
				app := filepath.Join(source, "app")
				if err := os.MkdirAll(filepath.Join(app, "scripts"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(app, "pubspec.yaml"), []byte("version: 2.3.4+56\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(app, "scripts/build.ps1"), []byte("Write-Output 'build'"), 0600); err != nil {
					t.Fatal(err)
				}
				if kind == "zip" {
					input := filepath.Join(t.TempDir(), "external.zip")
					if _, err := client.PrepareSource("folder", source, input); err != nil {
						t.Fatal(err)
					}
					source = input
				}
				var requests atomic.Int32
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					fail := func(err any) { t.Errorf("invalid submission: %v", err); http.Error(w, "invalid", 400) }
					if r.URL.Path != "/v1/jobs" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer test-key" {
						fail("route/auth")
						return
					}
					reader, err := r.MultipartReader()
					if err != nil {
						fail(err)
						return
					}
					part, err := reader.NextPart()
					if err != nil {
						fail(err)
						return
					}
					if part.FormName() != "spec" {
						fail("missing spec")
						return
					}
					var s model.Spec
					if err = json.NewDecoder(part).Decode(&s); err != nil {
						fail(err)
						return
					}
					if err = s.Validate(); err != nil {
						fail(err)
						return
					}
					if s.SourceType != kind || s.SourceSHA != "" || s.SourceDirty || s.Version != "2.3.4" || s.BuildNumber != "56" || s.WorkDir != "app" {
						fail(s)
						return
					}
					part, err = reader.NextPart()
					if err != nil {
						fail(err)
						return
					}
					if part.FormName() != "archive" {
						fail("missing archive")
						return
					}
					data, err := io.ReadAll(part)
					if err != nil {
						fail(err)
						return
					}
					if fmt.Sprintf("%x", sha256.Sum256(data)) != s.ArchiveSHA256 {
						fail("digest mismatch")
						return
					}
					file := filepath.Join(t.TempDir(), "received.zip")
					if err = os.WriteFile(file, data, 0600); err != nil {
						fail(err)
						return
					}
					if err = build.ValidateArchive(file); err != nil {
						fail(err)
						return
					}
					if _, err = client.SourceFile(file, "app/scripts/build.ps1"); err != nil {
						fail(err)
						return
					}
					if kind == "zip" {
						original, _ := os.ReadFile(source)
						if string(original) != string(data) {
							fail("ZIP changed in transit")
							return
						}
					}
					w.WriteHeader(http.StatusAccepted)
					_ = json.NewEncoder(w).Encode(model.Job{ID: "11111111-2222-4333-8444-555555555555", Status: "queued"})
				}))
				defer server.Close()
				config := t.TempDir()
				env := filepath.Join(config, "agent.env")
				pin := sha256.Sum256(server.Certificate().Raw)
				if err := os.WriteFile(env, fmt.Appendf(nil, "KILNBRIDGE_URL=%s\nKILNBRIDGE_KEY=test-key\nKILNBRIDGE_PIN_SHA256=%x\n", server.URL, pin), 0600); err != nil {
					t.Fatal(err)
				}
				spec := model.Spec{Recipe: recipe, Project: "demo", WorkDir: "app", Publish: "local"}
				if recipe == "custom" {
					spec.Version = "2.3.4"
					spec.BuildNumber = "56"
					spec.SkipTests = true
					spec.OutputDir = "release"
					spec.Commands = []model.Command{{Run: "powershell.exe", Args: []string{"-File", "scripts/build.ps1"}}}
				}
				data, _ := json.Marshal(spec)
				file := filepath.Join(config, "recipe.json")
				if err := os.WriteFile(file, data, 0600); err != nil {
					t.Fatal(err)
				}
				if err := run([]string{"submit", "--source-type", kind, "--source", source, "--recipe", file, "--env", env, "--detach"}); err != nil {
					t.Fatal(err)
				}
				if requests.Load() != 1 {
					t.Fatal("did not submit")
				}
			})
		}
	}
}

func TestCLISnapshotUsesIncludesAndWritesSourceMetadata(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fixture.pem"), []byte("synthetic fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	recipe := filepath.Join(t.TempDir(), "recipe.json")
	if err := os.WriteFile(recipe, []byte(`{"snapshotIncludes":["fixture.pem"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "source.zip")
	if err := run([]string{"snapshot", "--source-type", "folder", "--source", dir, "--recipe", recipe, "--output", output}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var info client.SourceInfo
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatal(err)
	}
	digest, _ := build.FileSHA256(output)
	if info.Type != "folder" || info.Digest != digest || info.SHA != "" {
		t.Fatal(info)
	}
	if _, err := client.SourceFile(output, "fixture.pem"); err != nil {
		t.Fatal(err)
	}
}
