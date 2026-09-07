//go:build windows

package platform

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func Protect(b []byte) ([]byte, error) {
	in := windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte{}, unsafe.Slice(out.Data, int(out.Size))...), nil
}
func Unprotect(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, fmt.Errorf("empty encrypted data")
	}
	in := windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte{}, unsafe.Slice(out.Data, int(out.Size))...), nil
}
func SetAutostart(enabled bool, exe string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if enabled {
		return k.SetStringValue("Kilnbridge", `"`+exe+`" --background`)
	}
	err = k.DeleteValue("Kilnbridge")
	if err == registry.ErrNotExist {
		return nil
	}
	return err
}
func Background(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS}
}
func PrepareCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	cmd.Cancel = func() error {
		kill := exec.Command("taskkill.exe", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
		kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		_ = kill.Run()
		return cmd.Process.Kill()
	}
}
func PSQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
func EncodedPS(script string) string {
	u := utf16.Encode([]rune(script))
	b := make([]byte, len(u)*2)
	for i, v := range u {
		binary.LittleEndian.PutUint16(b[i*2:], v)
	}
	return base64.StdEncoding.EncodeToString(b)
}
func ShellCommand(ctx context.Context, file string, args ...string) *exec.Cmd {
	if strings.HasSuffix(strings.ToLower(file), ".bat") || strings.HasSuffix(strings.ToLower(file), ".cmd") {
		script := "& " + PSQuote(file)
		for _, a := range args {
			script += " " + PSQuote(a)
		}
		script += "; exit $LASTEXITCODE"
		return exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-EncodedCommand", EncodedPS(script))
	}
	return exec.CommandContext(ctx, file, args...)
}
func OpenUI(address string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	w := webview2.NewWithOptions(webview2.WebViewOptions{AutoFocus: true, WindowOptions: webview2.WindowOptions{Title: "Kilnbridge", Width: 1160, Height: 850, Center: true}})
	if w == nil {
		// The agent does not depend on WebView2. Keep setup accessible on a PC
		// whose Windows installation does not include the runtime yet.
		return exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", address).Start()
	}
	defer w.Destroy()
	w.SetSize(760, 600, webview2.HintMin)
	w.Navigate(address)
	w.Run()
	return nil
}
func AttachConsole() {
	windows.NewLazySystemDLL("kernel32.dll").NewProc("AttachConsole").Call(^uintptr(0))
	if f, e := os.OpenFile("CONOUT$", os.O_WRONLY, 0); e == nil {
		os.Stdout = f
		os.Stderr = f
	}
}
func Alert(message string) {
	m, _ := windows.UTF16PtrFromString(message)
	title, _ := windows.UTF16PtrFromString("Kilnbridge")
	windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(title)), 0x10)
}
func WaitForPID(pid int, timeout time.Duration) error {
	h, e := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if e == windows.ERROR_INVALID_PARAMETER {
		return nil
	}
	if e != nil {
		return e
	}
	defer windows.CloseHandle(h)
	result, e := windows.WaitForSingleObject(h, uint32(timeout.Milliseconds()))
	if e != nil {
		return e
	}
	if result != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("timed out waiting for the old agent")
	}
	return nil
}

// Lock is released by Windows when the process exits, including after a crash.
func Lock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	var overlapped windows.Overlapped
	if err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func CreateShortcut(exe string) error {
	script := "$shortcut = (New-Object -ComObject WScript.Shell).CreateShortcut([Environment]::GetFolderPath('Programs') + '\\Kilnbridge.lnk'); $shortcut.TargetPath = " + PSQuote(exe) + "; $shortcut.Description = 'Kilnbridge build agent'; $shortcut.Save()"
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-EncodedCommand", EncodedPS(script))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Env = ChildEnvironment()
	return cmd.Run()
}
