package client

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

type downloadTransport func(*http.Request) (*http.Response, error)

func (f downloadTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDownloadStreamsLargeFilesAndRemovesFailedTransfers(t *testing.T) {
	data := bytes.Repeat([]byte("result-bytes"), 400000)
	sum := sha256.Sum256(data)
	id := "11111111-2222-4333-8444-555555555555"
	for _, kind := range []string{"success", "corrupt", "truncated", "unsafe-name", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			result := model.Result{Name: "demo.zip", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
			if kind == "unsafe-name" {
				result.Name = "../escape.zip"
			}
			calls := 0
			c := &Client{URL: "https://agent.example", Key: "synthetic-key", HTTP: &http.Client{Transport: downloadTransport(func(r *http.Request) (*http.Response, error) {
				if r.Context().Err() != nil {
					return nil, r.Context().Err()
				}
				if r.Header.Get("Authorization") != "Bearer synthetic-key" {
					t.Fatal("missing download authentication")
				}
				b := data
				if strings.HasSuffix(r.URL.Path, "/result") {
					calls++
					if kind == "corrupt" {
						b = append([]byte{}, data...)
						b[0] = 'X'
					}
					if kind == "truncated" {
						b = data[:100]
					}
				} else {
					b, _ = json.Marshal(map[string]any{"job": model.Job{ID: id, Result: &result}})
				}
				return &http.Response{StatusCode: 200, ContentLength: -1, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(b))}, nil
			})}}
			output := filepath.Join(t.TempDir(), "result.zip")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "cancelled" {
				cancel()
			}
			_, err := c.Download(ctx, id, output)
			if kind == "success" {
				if err != nil {
					t.Fatal(err)
				}
				b, _ := os.ReadFile(output)
				if !bytes.Equal(b, data) {
					t.Fatal("truncated large result")
				}
			} else {
				if err == nil {
					t.Fatal("accepted invalid download")
				}
				if _, err = os.Stat(output); !os.IsNotExist(err) {
					t.Fatal("kept partial download")
				}
			}
			if kind == "unsafe-name" && calls != 0 {
				t.Fatal("requested unsafe filename")
			}
		})
	}
}

func TestPublicationEnvironmentIsOptionalAndModeScoped(t *testing.T) {
	// Explicit dotenv values mask any real credentials in the test environment.
	env := map[string]string{"MINIO_ENDPOINT": "https://storage.example", "MINIO_BUCKET": "releases", "MINIO_ACCESS_KEY_ID": "key", "MINIO_SECRET_ACCESS_KEY": "secret", "MINIO_REGION": "", "MINIO_PREFIX": "", "MINIO_SESSION_TOKEN": "", "KILNBRIDGE_GITHUB_TOKEN": "github-secret"}
	for _, mode := range []string{"local", "download", "s3", "github", ""} {
		s := model.Spec{Publish: mode, GitHub: model.GitHubRelease{Repository: "owner/repo", Tag: "v1", Draft: true}}
		if err := PublicationFromEnv(&s, env); err != nil {
			t.Fatal(mode, err)
		}
		if (mode == "local" || mode == "download") && (s.Storage != (model.Storage{}) || s.GitHub != (model.GitHubRelease{})) {
			t.Fatal("forwarded unused secrets")
		}
		if mode == "github" && (s.GitHub.Token != "github-secret" || s.Storage != (model.Storage{})) {
			t.Fatal("incorrect GitHub credentials")
		}
		if (mode == "s3" || mode == "") && s.Publish != "s3" {
			t.Fatal("legacy publication changed")
		}
	}
	for key := range env {
		env[key] = ""
	}
	s := model.Spec{}
	if err := PublicationFromEnv(&s, env); err != nil || s.Publish != "local" {
		t.Fatal("storage-free mode requires credentials", err)
	}
}
