package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jourloy/kilnbridge/internal/agent"
)

func TestBackgroundExecutable(t *testing.T) {
	binary := os.Getenv("KILNBRIDGE_TEST_EXE")
	if binary == "" {
		t.Skip("set KILNBRIDGE_TEST_EXE to test a packaged executable")
	}
	dir := t.TempDir()
	// Avoid the default port and disable external update requests in the smoke test.
	config := `{"name":"Smoke PC","listen":"127.0.0.1:0","autoUpdate":false,"retentionDays":1,"repository":"Jourloy/kilnbridge"}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--background", "--data", dir)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("agent exit: %v", err)
			}
		case <-time.After(10 * time.Second):
			cancel()
			<-done
			t.Error("agent did not exit")
		}
	}()
	var address string
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		address, _ = agent.ExistingSettings(dir)
		if address != "" {
			break
		}
	}
	if address == "" {
		t.Fatal("packaged background agent did not become ready")
	}
	base, key, _ := strings.Cut(address, "/#")
	request := func(method, path string) []byte {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, method, base+path, nil)
		req.Header.Set("Authorization", "Bearer "+key)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 {
			t.Fatalf("%s: HTTP %d", path, res.StatusCode)
		}
		return body
	}
	var state struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(request("GET", "/admin/state"), &state); err != nil || state.Version == "" {
		t.Fatal("packaged UI state is invalid")
	}
	duplicate := exec.CommandContext(ctx, binary, "--background", "--data", dir)
	if err := duplicate.Run(); err == nil {
		t.Fatal("a second background agent started")
	}
	request("POST", "/admin/stop")
	// Cleanup waits for graceful exit and releases the per-user process lock.
}
