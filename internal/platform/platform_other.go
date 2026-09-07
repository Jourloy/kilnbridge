//go:build !windows

package platform

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"
)

func Protect(b []byte) ([]byte, error)   { return b, nil } // Development only; files are mode 0600.
func Unprotect(b []byte) ([]byte, error) { return b, nil }
func SetAutostart(enabled bool, exe string) error {
	if enabled {
		return errors.New("automatic startup is available on Windows")
	}
	return nil
}
func OpenUI(address string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	return exec.Command(name, address).Start()
}
func Background(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
func PrepareCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
func ShellCommand(ctx context.Context, file string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, file, args...)
}
func AttachConsole()       {}
func Alert(message string) {}
func WaitForPID(pid int, timeout time.Duration) error {
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return nil
		}
	}
	return errors.New("timed out waiting for the old agent")
}

func Lock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func CreateShortcut(exe string) error { return nil }
