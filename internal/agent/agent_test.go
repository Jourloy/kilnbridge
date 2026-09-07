package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
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

func specFor(t *testing.T, archive string) model.Spec {
	t.Helper()
	digest, e := build.FileSHA256(archive)
	if e != nil {
		t.Fatal(e)
	}
	return model.Spec{Project: "demo", WorkDir: ".", Version: "1.2.3", BuildNumber: "4", SourceSHA: strings.Repeat("a", 40), ArchiveSHA256: digest, APIURL: "https://api.example.com", Storage: model.Storage{Endpoint: "https://storage.example.com", Bucket: "releases", AccessKey: "access-key", SecretKey: "secret-key", Region: "us-east-1", Prefix: "release-candidates"}}
}
func fixtureApp(t *testing.T) (*App, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	a, e := NewApp(ctx, t.TempDir(), "0.1.0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { cancel(); a.M.Wait() })
	a.buildSupported = true
	return a, ctx
}
func archiveFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "source.zip")
	if e := os.WriteFile(p, []byte("snapshot"), 0600); e != nil {
		t.Fatal(e)
	}
	return p
}
func awaitStatus(t *testing.T, m *Manager, id, want string) model.Job {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		j, _ := m.Get(id)
		if j.Status == want {
			return j
		}
	}
	j, _ := m.Get(id)
	t.Fatalf("wanted %s, got %+v", want, j)
	return j
}
func TestPinnedSubmissionQueueUploadRetryAndSecretCleanup(t *testing.T) {
	a, ctx := fixtureApp(t)
	var builds, uploads atomic.Int32
	a.M.buildJob = func(ctx context.Context, r build.Runner, dir, id string, s model.Spec) ([]model.Artifact, error) {
		builds.Add(1)
		r.Log("message includes " + s.Storage.SecretKey)
		return []model.Artifact{{Name: "demo.zip", SHA256: strings.Repeat("b", 64)}}, nil
	}
	a.M.uploadJob = func(context.Context, string, string, model.Spec, []model.Artifact, func(string)) error {
		if uploads.Add(1) == 1 {
			return errors.New("upload unavailable with secret-key")
		}
		return nil
	}
	s := httptest.NewTLSServer(a.RemoteHandler())
	defer s.Close()
	sum := sha256.Sum256(s.Certificate().Raw)
	c, e := client.New(s.URL, a.M.secrets.Key, hex.EncodeToString(sum[:]))
	if e != nil {
		t.Fatal(e)
	}
	archive := archiveFile(t)
	spec := specFor(t, archive)
	a.M.Start()
	j, e := c.Submit(ctx, archive, spec)
	if e != nil {
		t.Fatal(e)
	}
	j = awaitStatus(t, a.M, j.ID, "upload_failed")
	if strings.Contains(j.Error, "secret-key") {
		t.Fatal("leaked credentials in status")
	}
	log, _ := a.M.Log(j.ID)
	if strings.Contains(log, "secret-key") || !strings.Contains(log, "[redacted]") {
		t.Fatalf("unsafe log: %s", log)
	}
	if _, e = os.Stat(filepath.Join(a.M.jobDir(j.ID), "spec.bin")); e != nil {
		t.Fatal("lost credentials required for retry")
	}
	if e = a.M.Retry(j.ID); e != nil {
		t.Fatal(e)
	}
	awaitStatus(t, a.M, j.ID, "succeeded")
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, e = os.Stat(filepath.Join(a.M.jobDir(j.ID), "spec.bin")); os.IsNotExist(e) {
			break
		}
	}
	if !os.IsNotExist(e) {
		t.Fatal("kept credentials after upload")
	}
	if builds.Load() != 1 || uploads.Load() != 2 {
		t.Fatal("retry unexpectedly rebuilt source")
	}
}
func TestAuthenticationSeparationAndKeyRotation(t *testing.T) {
	a, _ := fixtureApp(t)
	remote := a.RemoteHandler()
	local := a.LocalHandler("127.0.0.1:9000")
	request := func(h http.Handler, method, route, key, origin string) int {
		r := httptest.NewRequest(method, "http://127.0.0.1:9000"+route, nil)
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	old := a.M.secrets.Key
	if got := request(remote, "GET", "/v1/health", old, ""); got != 200 {
		t.Fatal(got)
	}
	for _, key := range []string{"", a.M.secrets.Admin} {
		if got := request(remote, "GET", "/v1/health", key, ""); got != 401 {
			t.Fatal("accepted wrong remote credential", got)
		}
	}
	if got := request(local, "GET", "/admin/key", old, ""); got != 401 {
		t.Fatal("remote key opened settings", got)
	}
	if got := request(local, "POST", "/admin/key/rotate", a.M.secrets.Admin, "https://attacker.example"); got != 403 {
		t.Fatal("accepted cross-origin mutation", got)
	}
	if got := request(local, "POST", "/admin/key/rotate", a.M.secrets.Admin, "http://127.0.0.1:9000"); got != 200 {
		t.Fatal(got)
	}
	if got := request(remote, "GET", "/v1/health", old, ""); got != 401 {
		t.Fatal("old key remains valid", got)
	}
	if got := request(remote, "GET", "/v1/health", a.M.secrets.Key, "https://attacker.example"); got != 401 {
		t.Fatal("remote browser origin accepted", got)
	}
}
func TestSubmissionRejectsWrongHashAndPause(t *testing.T) {
	a, ctx := fixtureApp(t)
	s := httptest.NewTLSServer(a.RemoteHandler())
	defer s.Close()
	sum := sha256.Sum256(s.Certificate().Raw)
	c, _ := client.New(s.URL, a.M.secrets.Key, hex.EncodeToString(sum[:]))
	archive := archiveFile(t)
	spec := specFor(t, archive)
	spec.ArchiveSHA256 = strings.Repeat("0", 64)
	if _, e := c.Submit(ctx, archive, spec); e == nil {
		t.Fatal("accepted corrupt upload")
	}
	if len(a.M.List()) != 0 {
		t.Fatal("queued corrupt source")
	}
	a.M.config.Paused = true
	if _, e := c.Submit(ctx, archive, spec); e == nil {
		t.Fatal("accepted upload while paused")
	}
}
func TestRestartMarksInterruptedJobAndRetainsQueuedJob(t *testing.T) {
	a, _ := fixtureApp(t)
	p := archiveFile(t)
	j, e := a.M.Add(p, specFor(t, p))
	if e != nil {
		t.Fatal(e)
	}
	a.M.change(j.ID, func(j *model.Job) { j.Status = "running" })
	m, e := NewManager(context.Background(), a.M.dir)
	if e != nil {
		t.Fatal(e)
	}
	got, _ := m.Get(j.ID)
	if got.Status != "failed" || !strings.Contains(got.Error, "stopped") {
		t.Fatal(got)
	}
	if _, e = os.Stat(filepath.Join(m.jobDir(j.ID), "spec.bin")); !os.IsNotExist(e) {
		t.Fatal("retained interrupted build credentials")
	}
}
func TestUpdaterWaitsForJobsAndVisibleSettings(t *testing.T) {
	a, _ := fixtureApp(t)
	if !a.M.ReserveUpdate() {
		t.Fatal("idle agent could not reserve update")
	}
	if e := a.M.Available(); e == nil {
		t.Fatal("accepted work during update")
	}
	a.M.ReleaseUpdate()
	a.M.uiSeen = time.Now()
	if a.M.ReserveUpdate() {
		t.Fatal("updated while settings were visible")
	}
	a.M.uiSeen = time.Time{}
	p := archiveFile(t)
	if _, e := a.M.Add(p, specFor(t, p)); e != nil {
		t.Fatal(e)
	}
	if a.M.ReserveUpdate() {
		t.Fatal("updated with queued work")
	}
}
func TestPublicJobMetadataDoesNotContainCredentials(t *testing.T) {
	a, _ := fixtureApp(t)
	p := archiveFile(t)
	j, e := a.M.Add(p, specFor(t, p))
	if e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(filepath.Join(a.M.jobDir(j.ID), "job.json"))
	var fields map[string]any
	if e = json.Unmarshal(b, &fields); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(b), "secret-key") || fields["storage"] != nil {
		t.Fatal("public job metadata contains secrets")
	}
}
