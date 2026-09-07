//go:build windows

package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jourloy/kilnbridge/internal/platform"
)

// The test executable doubles as a minimal replacement worker. This exercises
// real Windows executable replacement, process startup, and rollback.
func TestMain(m *testing.M) {
	if os.Getenv("KILNBRIDGE_UPDATE_FIXTURE") == "1" {
		var data, nonce string
		for i, arg := range os.Args[1:] {
			if arg == "--exit" {
				os.Exit(0)
			}
			if arg == "--data" && i+2 < len(os.Args) {
				data = os.Args[i+2]
			}
			if arg == "--update-nonce" && i+2 < len(os.Args) {
				nonce = os.Args[i+2]
			}
		}
		if data == "" {
			os.Exit(2)
		}
		if nonce != "" && os.Getenv("KILNBRIDGE_UPDATE_FAIL") == "1" {
			os.Exit(3)
		}
		if nonce != "" {
			_ = os.WriteFile(filepath.Join(data, "updates", "healthy-"+nonce), []byte("1.2.3"), 0600)
		}
		_ = os.WriteFile(filepath.Join(data, "fixture.pid"), []byte(strconv.Itoa(os.Getpid())), 0600)
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestWindowsReplacementAndRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint("failed-startup=", fail), func(t *testing.T) {
			t.Setenv("KILNBRIDGE_UPDATE_FIXTURE", "1")
			t.Setenv("KILNBRIDGE_UPDATE_FAIL", "0")
			if fail {
				t.Setenv("KILNBRIDGE_UPDATE_FAIL", "1")
			}
			data := t.TempDir()
			exe, _ := os.Executable()
			target := InstalledPath(data)
			if err := CopyFile(exe, target); err != nil {
				t.Fatal(err)
			}
			stage := filepath.Join(data, "updates", "1.2.3")
			staged := filepath.Join(stage, AssetName)
			if err := CopyFile(exe, staged); err != nil {
				t.Fatal(err)
			}
			// PE overlays distinguish the old/new binaries without changing execution.
			f, err := os.OpenFile(staged, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = f.WriteString("new release overlay")
			_ = f.Close()
			pub, priv, _ := ed25519.GenerateKey(rand.Reader)
			raw, sig, err := Sign(staged, "1.2.3", priv)
			if err != nil {
				t.Fatal(err)
			}
			_ = os.WriteFile(filepath.Join(stage, "update.json"), raw, 0600)
			_ = os.WriteFile(filepath.Join(stage, "update.json.sig"), sig, 0600)
			parent := exec.Command(exe, "--exit")
			if err := parent.Start(); err != nil {
				t.Fatal(err)
			}
			pid := parent.Process.Pid
			_ = parent.Wait()
			t.Cleanup(func() {
				for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
					b, err := os.ReadFile(filepath.Join(data, "fixture.pid"))
					if err == nil {
						childPID, _ := strconv.Atoi(string(b))
						if childPID > 0 {
							if p, err := os.FindProcess(childPID); err == nil {
								_ = p.Kill()
								_ = platform.WaitForPID(childPID, 5*time.Second)
							}
						}
						return
					}
				}
			})
			err = applyWithKey(data, "1.2.3", pid, pub, 3*time.Second)
			if fail && (err == nil || !strings.Contains(err.Error(), "restored the previous version")) {
				t.Fatalf("rollback did not run: %v", err)
			}
			if !fail && err != nil {
				t.Fatal(err)
			}
			expected := staged
			if fail {
				expected = exe
			}
			expectedBytes, _ := os.ReadFile(expected)
			actualBytes, _ := os.ReadFile(target)
			if string(expectedBytes) != string(actualBytes) {
				t.Fatal("wrong executable after update")
			}
		})
	}
}
