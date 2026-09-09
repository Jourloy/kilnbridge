package build

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jourloy/kilnbridge/internal/model"
)

type githubTransport func(*http.Request) (*http.Response, error)

func (f githubTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGitHubReleaseCreationUploadRetryAndPublication(t *testing.T) {
	for _, scenario := range []string{"publish", "draft", "upload-retry", "conflict", "bad-url", "forbidden", "missing-commit", "immutable-retry", "canonical-case"} {
		t.Run(scenario, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "result.zip")
			data := []byte("synthetic result bundle")
			if err := os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256(data)
			digest := hex.EncodeToString(hash[:])
			result := model.Result{Name: "demo-1.2.3-11111111-2222-4333-8444-555555555555.zip", Size: int64(len(data)), SHA256: digest}
			s := model.Spec{Publish: "github", SourceSHA: strings.Repeat("a", 40), GitHub: model.GitHubRelease{Repository: "owner/repo", Tag: "v1.2.3", Token: "fake-token", Draft: scenario == "draft"}}
			var release *githubRelease
			var asset *githubAsset
			creates, uploads, deletes, publishes := 0, 0, 0, 0
			client := &http.Client{Transport: githubTransport(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Authorization") != "Bearer fake-token" || r.Header.Get("X-GitHub-Api-Version") == "" {
					t.Fatal("missing API headers")
				}
				status := 200
				var response any
				if scenario == "forbidden" {
					status = 403
				} else {
					switch {
					case r.Method == "GET" && strings.Contains(r.URL.Path, "/releases/tags/"):
						if release != nil && !release.Draft {
							response = release
						} else {
							status = 404
						}
					case r.Method == "GET" && r.URL.Path == "/repos/owner/repo/releases":
						response = []githubRelease{}
						if release != nil {
							response = []githubRelease{*release}
						}
					case r.Method == "GET" && strings.Contains(r.URL.Path, "/git/ref/tags/"):
						status = 404
					case r.Method == "GET" && strings.Contains(r.URL.Path, "/commits/"):
						response = map[string]string{"sha": s.SourceSHA}
						if scenario == "missing-commit" {
							status = 404
						}
					case r.Method == "POST" && r.URL.Path == "/repos/owner/repo/releases":
						creates++
						var body map[string]any
						_ = json.NewDecoder(r.Body).Decode(&body)
						if body["draft"] != true || body["target_commitish"] != s.SourceSHA {
							t.Fatal("created a premature or unrelated release")
						}
						release = &githubRelease{ID: 10, Tag: s.GitHub.Tag, Draft: true, UploadURL: "https://uploads.github.com/repos/owner/repo/releases/10/assets{?name,label}"}
						if scenario == "canonical-case" {
							release.UploadURL = "https://uploads.github.com/repos/Owner/Repo/releases/10/assets{?name,label}"
						}
						if scenario == "bad-url" {
							release.UploadURL = "https://attacker.example/upload"
						}
						if scenario == "conflict" {
							asset = &githubAsset{ID: 20, Name: result.Name, State: "uploaded", Digest: "sha256:other", Size: result.Size}
						}
						status = 201
						response = release
					case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/assets"):
						response = []githubAsset{}
						if asset != nil {
							response = []githubAsset{*asset}
						}
					case r.Method == "DELETE":
						deletes++
						asset = nil
						status = 204
					case r.Method == "POST" && r.URL.Host == "uploads.github.com":
						uploads++
						b, _ := io.ReadAll(r.Body)
						if !bytes.Equal(b, data) || r.ContentLength != result.Size || r.URL.Query().Get("name") != result.Name || r.Header.Get("Content-Type") != "application/zip" {
							t.Fatal("invalid asset upload")
						}
						asset = &githubAsset{ID: 20, Name: result.Name, State: "uploaded", Size: result.Size, Digest: "sha256:" + digest}
						status = 201
						response = asset
						if scenario == "upload-retry" && uploads == 1 {
							asset.State = "starter"
							asset.Size = 0
							status = 502
						}
					case r.Method == "PATCH":
						if asset == nil || asset.State != "uploaded" {
							t.Fatal("published incomplete release")
						}
						publishes++
						release.Draft = false
						if scenario == "immutable-retry" {
							release.Immutable = true
						}
						response = release
					default:
						t.Fatalf("unexpected request %s %s", r.Method, r.URL)
					}
				}
				b, _ := json.Marshal(response)
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(b))}, nil
			})}
			_, err := publishGitHub(context.Background(), client, file, s, result, nil)
			if scenario == "upload-retry" {
				if err == nil || publishes != 0 {
					t.Fatal("upload failure not retained as draft")
				}
				_, err = publishGitHub(context.Background(), client, file, s, result, nil)
				if deletes != 1 || creates != 1 || uploads != 2 {
					t.Fatal("retry did not reuse release", deletes, creates, uploads)
				}
			}
			if scenario == "conflict" || scenario == "bad-url" || scenario == "forbidden" || scenario == "missing-commit" {
				if err == nil || uploads != 0 || deletes != 0 || publishes != 0 {
					t.Fatal("unsafe publication", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "draft" && publishes != 0 {
				t.Fatal("published a requested draft")
			}
			before := uploads
			_, err = publishGitHub(context.Background(), client, file, s, result, nil)
			if err != nil || uploads != before || creates != 1 {
				t.Fatal("retry reuploaded a verified asset", err)
			}
		})
	}
}
