//go:build windows

package platform

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestTrayWindowsLayouts(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("Kilnbridge targets Windows x64")
	}
	for name, sizes := range map[string][2]uintptr{
		"WNDCLASSEXW":     {unsafe.Sizeof(windowClass{}), 80},
		"MSG":             {unsafe.Sizeof(windowMessage{}), 48},
		"NOTIFYICONDATAW": {unsafe.Sizeof(notifyIconData{}), 976},
	} {
		if sizes[0] != sizes[1] {
			t.Errorf("%s size = %d, want %d", name, sizes[0], sizes[1])
		}
	}
}

// Exercise the real Win32 window, message pump and embedded icon. Only the
// Explorer notification call is replaced: Windows CI need not have a taskbar.
func TestTrayLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type notification struct {
		message uintptr
		data    notifyIconData
	}
	notifications := make(chan notification, 32)
	opened := make(chan struct{}, 4)
	done := make(chan error, 1)
	var attempts atomic.Int32
	go func() {
		done <- runTray(ctx, func() error {
			opened <- struct{}{}
			return nil
		}, func() error { cancel(); return nil }, func(message uintptr, data *notifyIconData) bool {
			notifications <- notification{message, *data}
			// Reproduce Explorer not being ready at sign-in.
			return message != nimAdd || attempts.Add(1) > 1
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("tray message loop did not exit on shutdown")
		}
	})
	next := func(want uintptr) notifyIconData {
		t.Helper()
		select {
		case got := <-notifications:
			if got.message != want {
				t.Fatalf("notification = %d, want %d", got.message, want)
			}
			return got.data
		case <-time.After(5 * time.Second):
			t.Fatalf("missing notification %d", want)
			return notifyIconData{}
		}
	}
	data := next(nimAdd)
	if data.Window == 0 || data.Icon == 0 || data.Callback != wmTray || data.Version != 4 {
		t.Fatal("tray icon is not connected to its native window")
	}
	if windows.UTF16ToString(data.Tip[:]) != "Kilnbridge — background agent is running" {
		t.Fatal("tray tooltip does not explain the background worker")
	}
	hwnd := uintptr(data.Window)
	postMessage.Call(hwnd, wmTimer, 1, 0)
	next(nimAdd)
	next(nimSetVersion)
	for _, event := range []uintptr{ninSelect, ninKeySelect} {
		postMessage.Call(hwnd, wmTray, 0, uintptr(data.ID)<<16|event)
		select {
		case <-opened:
		case <-time.After(5 * time.Second):
			t.Fatal("tray activation did not reopen settings")
		}
	}
	message, _, _ := user32.NewProc("RegisterWindowMessageW").Call(uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("TaskbarCreated"))))
	postMessage.Call(hwnd, message, 0, 0)
	next(nimAdd)
	next(nimSetVersion)
	// The same action used by the popup menu must stop and remove the icon.
	value, ok := trayWindows.Load(hwnd)
	if !ok {
		t.Fatal("tray window was lost")
	}
	value.(*trayWindow).action(trayStop)
	next(nimDelete)
}

func TestSettingsWindowRestoresExistingHost(t *testing.T) {
	name := settingsWindowName("http://127.0.0.1:12345/#synthetic-admin-key")
	lock, err := claimSettingsWindow(name)
	if err != nil || lock == 0 {
		t.Fatalf("claim first host: %v", err)
	}
	defer func() { windows.CloseHandle(lock) }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan uintptr, 1)
	done := make(chan error, 1)
	go func() {
		done <- runTray(ctx, func() error { return nil }, func() error { return nil }, func(message uintptr, data *notifyIconData) bool {
			if message == nimAdd {
				ready <- uintptr(data.Window)
			}
			return true
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("test window did not close")
		}
	})
	var hwnd uintptr
	select {
	case hwnd = <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("test window did not start")
	}
	// Use the native fixture as a settings host, without requiring WebView2.
	if ok, _, err := user32.NewProc("SetPropW").Call(hwnd, uintptr(unsafe.Pointer(name)), 1); ok == 0 {
		t.Fatal(err)
	}
	if duplicate, err := claimSettingsWindow(name); duplicate != 0 || err != nil {
		if duplicate != 0 {
			windows.CloseHandle(duplicate)
		}
		t.Fatalf("created a duplicate settings host: %v", err)
	}
	waitUntil := func(proc string, want bool) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			result, _, _ := user32.NewProc(proc).Call(hwnd)
			if (result != 0) == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s did not become %v", proc, want)
			}
		}
	}
	waitUntil("IsWindowVisible", true)
	user32.NewProc("ShowWindowAsync").Call(hwnd, 6) // SW_MINIMIZE
	waitUntil("IsIconic", true)
	if !focusSettingsWindow(name) {
		t.Fatal("could not find minimized settings")
	}
	waitUntil("IsIconic", false)
	if focusSettingsWindow(settingsWindowName("another-worker")) {
		t.Fatal("focused settings belonging to another worker")
	}
	windows.CloseHandle(lock)
	// After the settings process closes, another host can claim the same name.
	lock, err = claimSettingsWindow(name)
	if err != nil || lock == 0 {
		t.Fatalf("claim a new host: %v", err)
	}
}
