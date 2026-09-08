//go:build windows

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jourloy/kilnbridge/internal/platform"
	"golang.org/x/sys/windows"
)

// The child probes the startup flags Windows actually receives, without needing
// WebView2 or an interactive desktop on the CI runner.
func TestMain(m *testing.M) {
	if os.Getenv("KILNBRIDGE_TEST_UI_LAUNCH") == "1" && len(os.Args) == 3 && os.Args[1] == "--data" {
		var info windows.StartupInfo
		if err := windows.GetStartupInfo(&info); err != nil {
			os.Exit(2)
		}
		data, err := json.Marshal(struct {
			Flags      uint32
			ShowWindow uint16
		}{info.Flags, info.ShowWindow})
		if err != nil || os.WriteFile(filepath.Join(os.Args[2], "startup-info.json"), data, 0600) != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestInstalledUIStartupVisibility(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KILNBRIDGE_TEST_UI_LAUNCH", "1")
	for _, background := range []bool{false, true} {
		name := "settings"
		if background {
			name = "background worker"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if background {
				cmd := exec.Command(exe, "--data", dir)
				platform.Background(cmd)
				err = cmd.Run()
			} else {
				err = startInstalledUI(exe, dir)
			}
			if err != nil {
				t.Fatal(err)
			}
			var info struct {
				Flags      uint32
				ShowWindow uint16
			}
			for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
				data, readErr := os.ReadFile(filepath.Join(dir, "startup-info.json"))
				if readErr == nil && json.Unmarshal(data, &info) == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("child did not report its Windows startup flags")
				}
			}
			hidden := info.Flags&windows.STARTF_USESHOWWINDOW != 0 && info.ShowWindow == windows.SW_HIDE
			if hidden != background {
				t.Fatalf("hidden = %v, want %v (flags %#x, show window %d)", hidden, background, info.Flags, info.ShowWindow)
			}
		})
	}
}
