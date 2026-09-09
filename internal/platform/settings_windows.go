//go:build windows

package platform

import (
	"crypto/sha256"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func settingsWindowName(address string) *uint16 {
	// The hash distinguishes workers without putting the admin key in a public
	// window title, property name, or kernel object name.
	return windows.StringToUTF16Ptr(fmt.Sprintf(`Local\Kilnbridge.Settings.%x`, sha256.Sum256([]byte(address))))
}

// Holding a named object prevents rapid tray clicks from creating duplicate
// WebView2 hosts while the first window is still being initialized.
func claimSettingsWindow(name *uint16) (windows.Handle, error) {
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		lock, err := windows.CreateMutex(nil, false, name)
		if err == nil {
			return lock, nil
		}
		if err != windows.ERROR_ALREADY_EXISTS {
			return 0, err
		}
		windows.CloseHandle(lock)
		if focusSettingsWindow(name) {
			return 0, nil
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("the existing settings window is still starting; try opening Kilnbridge again")
		}
	}
}

func focusSettingsWindow(name *uint16) bool {
	find := user32.NewProc("FindWindowExW")
	getProp := user32.NewProc("GetPropW")
	var hwnd uintptr
	for {
		hwnd, _, _ = find.Call(0, hwnd, 0, 0)
		if hwnd == 0 {
			return false
		}
		if found, _, _ := getProp.Call(hwnd, uintptr(unsafe.Pointer(name))); found != 0 {
			// ShowWindowAsync cannot block this launcher if the UI is busy. Preserve
			// maximized windows and restore only those that are minimized.
			show := uintptr(5) // SW_SHOW
			if minimized, _, _ := user32.NewProc("IsIconic").Call(hwnd); minimized != 0 {
				show = 9 // SW_RESTORE
			}
			user32.NewProc("ShowWindowAsync").Call(hwnd, show)
			setForeground.Call(hwnd)
			return true
		}
	}
}
