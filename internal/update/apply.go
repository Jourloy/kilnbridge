package update

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/jourloy/kilnbridge/internal/platform"
)

func InstalledPath(data string) string { return filepath.Join(data, "app", "kilnbridge.exe") }
func CopyFile(src, dst string) error {
	r, e := os.Open(src)
	if e != nil {
		return e
	}
	defer r.Close()
	if e = os.MkdirAll(filepath.Dir(dst), 0700); e != nil {
		return e
	}
	w, e := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if e != nil {
		return e
	}
	_, e = io.Copy(w, r)
	if e == nil {
		e = w.Sync()
	}
	closeErr := w.Close()
	if e == nil {
		e = closeErr
	}
	if e != nil {
		_ = os.Remove(dst)
	}
	return e
}

// A copy of the old, trusted executable performs the replacement after the
// agent exits. It verifies the signed metadata again instead of trusting CLI
// arguments, and retains the old executable until startup is acknowledged.
func Apply(data, version string, parent int) error {
	return applyWithKey(data, version, parent, PublicKey(), time.Minute)
}

func applyWithKey(data, version string, parent int, key ed25519.PublicKey, startupTimeout time.Duration) error {
	if _, e := parseVersion(version); e != nil {
		return e
	}
	directory := filepath.Join(data, "updates", version)
	raw, e := os.ReadFile(filepath.Join(directory, "update.json"))
	if e != nil {
		return e
	}
	sig, e := os.ReadFile(filepath.Join(directory, "update.json.sig"))
	if e != nil {
		return e
	}
	m, e := Verify(raw, sig, key)
	if e != nil {
		return e
	}
	if m.Version != version {
		return errors.New("staged update version mismatch")
	}
	staged := filepath.Join(directory, AssetName)
	if e = VerifyFile(staged, m); e != nil {
		return e
	}
	if e = platform.WaitForPID(parent, time.Minute); e != nil {
		return e
	}
	target := InstalledPath(data)
	backup := target + ".previous"
	_ = os.Remove(backup)
	deadline := time.Now().Add(time.Minute)
	for {
		e = os.Rename(target, backup)
		if e == nil {
			break
		}
		if time.Now().After(deadline) {
			restart(target, data, "")
			return fmt.Errorf("could not replace the running application: %w", e)
		}
		time.Sleep(time.Second)
	}
	rollback := func() { _ = os.Remove(target); _ = os.Rename(backup, target); restart(target, data, "") }
	if e = CopyFile(staged, target); e != nil {
		rollback()
		return e
	}
	nonce := platform.Token()
	health := filepath.Join(data, "updates", "healthy-"+nonce)
	cmd, e := restart(target, data, nonce)
	if e != nil {
		rollback()
		return e
	}
	for deadline := time.Now().Add(startupTimeout); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if acknowledged, readErr := os.ReadFile(health); readErr == nil && string(acknowledged) == m.Version {
			_ = os.Remove(health)
			_ = os.Remove(backup)
			return nil
		}
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	rollback()
	return errors.New("new version did not become healthy; restored the previous version")
}
func restart(exe, data, nonce string) (*exec.Cmd, error) {
	args := []string{"--background", "--data", data}
	if nonce != "" {
		args = append(args, "--update-nonce", nonce)
	}
	cmd := exec.Command(exe, args...)
	platform.Background(cmd)
	e := cmd.Start()
	return cmd, e
}

func StartHelper(data string, p *Pending) error {
	exe, e := os.Executable()
	if e != nil {
		return e
	}
	helper := filepath.Join(data, "updates", "apply-"+platform.Token()+".exe")
	if e = CopyFile(exe, helper); e != nil {
		return e
	}
	cmd := exec.Command(helper, "apply-update", "--data", data, "--version", p.Version, "--parent", fmt.Sprint(os.Getpid()))
	platform.Background(cmd)
	if e = cmd.Start(); e != nil {
		_ = os.Remove(helper)
	}
	return e
}
