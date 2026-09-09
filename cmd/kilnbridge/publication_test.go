package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jourloy/kilnbridge/internal/client"
	"github.com/jourloy/kilnbridge/internal/model"
)

func TestCLIWaitAutomaticallyDownloadsAndStandaloneDownloadWorks(t *testing.T) {
	for _, mode := range []string{"download", "local", "failed-publication", "standalone"} {
		t.Run(mode, func(t *testing.T) {
			data := []byte("synthetic result ZIP")
			digest := sha256.Sum256(data)
			job := model.Job{ID: "11111111-2222-4333-8444-555555555555", Publish: mode, Status: "succeeded", Destination: "agent result", Result: &model.Result{Name: "demo.zip", Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:])}}
			if mode == "failed-publication" {
				job.Publish = "s3"
				job.Status = "upload_failed"
				job.Error = "storage offline"
			}
			requests := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer synthetic-key" {
					t.Error("missing authentication")
					w.WriteHeader(401)
					return
				}
				if strings.HasSuffix(r.URL.Path, "/result") {
					requests++
					_, _ = w.Write(data)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"job": job, "log": ""})
			}))
			defer server.Close()
			pin := sha256.Sum256(server.Certificate().Raw)
			c, err := client.New(server.URL, "synthetic-key", hex.EncodeToString(pin[:]))
			if err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(t.TempDir(), "result.zip")
			if mode == "standalone" {
				env := filepath.Join(t.TempDir(), "agent.env")
				values := fmt.Sprintf("KILNBRIDGE_URL=%s\nKILNBRIDGE_KEY=synthetic-key\nKILNBRIDGE_PIN_SHA256=%x\n", server.URL, pin)
				if err = os.WriteFile(env, []byte(values), 0600); err != nil {
					t.Fatal(err)
				}
				err = run([]string{"download", "--job", job.ID, "--env", env, "--output", output})
			} else {
				err = waitAndDownload(context.Background(), c, job.ID, output, mode == "failed-publication")
			}
			if mode == "failed-publication" {
				if err == nil {
					t.Fatal("publication failure hidden")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if mode == "local" {
				if requests != 0 {
					t.Fatal("downloaded without request")
				}
			} else {
				b, err := os.ReadFile(output)
				if err != nil || string(b) != string(data) || requests != 1 {
					t.Fatal("CLI did not save the result", err, requests)
				}
			}
		})
	}
}
