package agent

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jourloy/kilnbridge/internal/build"
	"github.com/jourloy/kilnbridge/internal/client"
	"github.com/jourloy/kilnbridge/internal/model"
)

func TestGitHubFailureKeepsResultsAndRedactsTokenForRetry(t *testing.T) {
	a, _ := fixtureApp(t)
	var builds, publishes atomic.Int32
	a.M.buildJob = func(ctx context.Context, r build.Runner, dir, id string, s model.Spec) ([]model.Artifact, error) {
		builds.Add(1)
		return localBuild(ctx, r, dir, id, s)
	}
	a.M.githubJob = func(ctx context.Context, file string, s model.Spec, result model.Result, log func(string)) (string, error) {
		log("Token: " + s.GitHub.Token)
		if publishes.Add(1) == 1 {
			return "", errors.New("GitHub rejected " + s.GitHub.Token)
		}
		return "https://github.com/owner/repo/releases/tag/v1", nil
	}
	archive := archiveFile(t)
	s := specFor(t, archive)
	s.Publish = "github"
	s.Storage = model.Storage{}
	s.GitHub = model.GitHubRelease{Repository: "owner/repo", Tag: "v1", Token: "synthetic-github-secret"}
	a.M.Start()
	j, err := a.M.Add(archive, s)
	if err != nil {
		t.Fatal(err)
	}
	j = awaitStatus(t, a.M, j.ID, "upload_failed")
	if j.Result == nil {
		t.Fatal("lost result after failed publication")
	}
	log, _ := a.M.Log(j.ID)
	if strings.Contains(j.Error, s.GitHub.Token) || strings.Contains(log, s.GitHub.Token) || !strings.Contains(log, "[redacted]") {
		t.Fatal("token leaked")
	}
	if _, err = os.Stat(filepath.Join(a.M.jobDir(j.ID), "spec.bin")); err != nil {
		t.Fatal("lost retry credentials")
	}
	if err = a.M.Retry(j.ID); err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, a.M, j.ID, "succeeded")
	if builds.Load() != 1 || publishes.Load() != 2 {
		t.Fatal("retry rebuilt source")
	}
}

func localBuild(ctx context.Context, r build.Runner, dir, id string, s model.Spec) ([]model.Artifact, error) {
	r.Log("A normal build log without any publication credentials.")
	out := filepath.Join(dir, "artifacts", "reports")
	if err := os.MkdirAll(out, 0700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(out, "junit.xml"), []byte("<testsuite/>"), 0600); err != nil {
		return nil, err
	}
	return build.Manifest(filepath.Dir(out), id, s)
}

func TestStorageFreeBuildCanBeDownloadedAndSurvivesRestart(t *testing.T) {
	for _, mode := range []string{"local", "download"} {
		t.Run(mode, func(t *testing.T) {
			a, ctx := fixtureApp(t)
			a.M.buildJob = localBuild
			a.M.uploadJob = func(context.Context, string, string, model.Spec, []model.Artifact, func(string)) error {
				return errors.New("local jobs must not publish")
			}
			server := httptest.NewTLSServer(a.RemoteHandler())
			defer server.Close()
			sum := sha256.Sum256(server.Certificate().Raw)
			c, err := client.New(server.URL, a.M.secrets.Key, hex.EncodeToString(sum[:]))
			if err != nil {
				t.Fatal(err)
			}
			archive := archiveFile(t)
			spec := specFor(t, archive)
			spec.Storage = model.Storage{}
			spec.Publish = mode
			a.M.Start()
			j, err := c.Submit(ctx, archive, spec)
			if err != nil {
				t.Fatal(err)
			}
			j = awaitStatus(t, a.M, j.ID, "succeeded")
			if j.Result == nil || j.Publish != mode || !strings.HasSuffix(j.Destination, "artifacts") {
				t.Fatal(j)
			}
			output := filepath.Join(t.TempDir(), "result.zip")
			if _, err = c.Download(ctx, j.ID, output); err != nil {
				t.Fatal(err)
			}
			digest, err := build.FileSHA256(output)
			if err != nil || digest != j.Result.SHA256 {
				t.Fatal("download hash", err)
			}
			z, err := zip.OpenReader(output)
			if err != nil {
				t.Fatal(err)
			}
			defer z.Close()
			if len(z.File) != 3 || z.File[0].Name != "reports/junit.xml" {
				t.Fatal("lost result layout")
			}
			log, _ := a.M.Log(j.ID)
			if strings.Contains(log, "[redacted]") || !strings.Contains(log, "normal build log") {
				t.Fatal(log)
			}
			for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
				if _, err = os.Stat(filepath.Join(a.M.jobDir(j.ID), "spec.bin")); os.IsNotExist(err) {
					break
				}
			}
			if !os.IsNotExist(err) {
				t.Fatal("retained job settings after local completion")
			}
			restarted, err := NewManager(context.Background(), a.M.dir)
			if err != nil {
				t.Fatal(err)
			}
			saved, ok := restarted.Get(j.ID)
			if !ok || saved.Result == nil || saved.Result.SHA256 != j.Result.SHA256 {
				t.Fatal("lost retained result")
			}
			if _, err = c.Download(ctx, j.ID, output); err == nil {
				t.Fatal("overwrote existing output")
			}
			request := httptest.NewRequest("GET", "/v1/jobs/"+j.ID+"/result", nil)
			rec := httptest.NewRecorder()
			a.RemoteHandler().ServeHTTP(rec, request)
			if rec.Code != 401 {
				t.Fatal("unauthenticated result download", rec.Code)
			}
		})
	}
}

func TestDownloadsHoldRetentionAndShutdownAndNeverExposeSources(t *testing.T) {
	a, _ := fixtureApp(t)
	a.M.buildJob = localBuild
	archive := archiveFile(t)
	spec := specFor(t, archive)
	spec.Storage = model.Storage{}
	j, err := a.M.Add(archive, spec)
	if err != nil {
		t.Fatal(err)
	}
	a.M.execute(context.Background(), j)
	a.M.change(j.ID, func(j *model.Job) { j.UpdatedAt = time.Now().Add(-10 * 24 * time.Hour) })
	// change updates the timestamp; simulate a retained job older than the limit.
	a.M.mu.Lock()
	old := a.M.jobs[j.ID]
	old.UpdatedAt = time.Now().Add(-10 * 24 * time.Hour)
	a.M.jobs[j.ID] = old
	a.M.mu.Unlock()
	if _, err = a.M.beginDownload(j.ID); err != nil {
		t.Fatal(err)
	}
	if a.M.ReserveStop() || a.M.ReserveUpdate() {
		t.Fatal("interrupted a download")
	}
	a.M.prune()
	if _, ok := a.M.Get(j.ID); !ok {
		t.Fatal("pruned active download")
	}
	a.M.endDownload(j.ID)
	a.M.prune()
	if _, ok := a.M.Get(j.ID); ok {
		t.Fatal("expired result retained")
	}
	for _, route := range []string{"/v1/jobs/" + j.ID + "/result", "/v1/jobs/" + j.ID + "/source.zip", "/v1/jobs/" + j.ID + "/spec.bin"} {
		r := httptest.NewRequest("GET", route, nil)
		r.Header.Set("Authorization", "Bearer "+a.M.secrets.Key)
		w := httptest.NewRecorder()
		a.RemoteHandler().ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatal(route, w.Code)
		}
	}
}
