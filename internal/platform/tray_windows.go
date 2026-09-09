//go:build windows

package platform

import (
	"context"
	_ "embed"
	"encoding/binary"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	wmClose       = 0x0010
	wmTimer       = 0x0113
	wmContextMenu = 0x007b
	wmTray        = 0x8001
	ninSelect     = 0x0400
	ninKeySelect  = 0x0401
	nimAdd        = 0
	nimDelete     = 2
	nimSetFocus   = 3
	nimSetVersion = 4
	trayOpen      = 1
	trayStop      = 2
)

var (
	user32         = windows.NewLazySystemDLL("user32.dll")
	shellNotify    = windows.NewLazySystemDLL("shell32.dll").NewProc("Shell_NotifyIconW")
	defWindowProc  = user32.NewProc("DefWindowProcW")
	postMessage    = user32.NewProc("PostMessageW")
	destroyWindow  = user32.NewProc("DestroyWindow")
	setForeground  = user32.NewProc("SetForegroundWindow")
	trayClassName  = windows.StringToUTF16Ptr("Kilnbridge.Tray")
	trayClassOnce  sync.Once
	trayClassError error
	trayWindows    sync.Map
	trayCallback   = windows.NewCallback(trayWindowProc)
)

// tray.ico is the settings UI's mark.svg rasterized at 32px, embedded so the
// downloaded standalone executable does not depend on adjacent asset files.
//
//go:embed tray.ico
var trayIcon []byte

// These layouts follow the Windows SDK's WNDCLASSEXW, MSG and NOTIFYICONDATAW.
type windowClass struct {
	Size, Style        uint32
	Proc               uintptr
	ClassExtra, Extra  int32
	Instance, Icon     windows.Handle
	Cursor, Background windows.Handle
	Menu, Name         *uint16
	SmallIcon          windows.Handle
}

type windowMessage struct {
	Window         windows.Handle
	ID             uint32
	WParam, LParam uintptr
	Time           uint32
	X, Y           int32
	Private        uint32
}

type notifyIconData struct {
	Size                uint32
	Window              windows.Handle
	ID, Flags, Callback uint32
	Icon                windows.Handle
	Tip                 [128]uint16
	State, StateMask    uint32
	Info                [256]uint16
	Version             uint32
	InfoTitle           [64]uint16
	InfoFlags           uint32
	GUID                windows.GUID
	BalloonIcon         windows.Handle
}

type trayWindow struct {
	hwnd           uintptr
	icon           notifyIconData
	taskbarCreated uint32
	added          bool
	open, stop     func() error
	notify         func(uintptr, *notifyIconData) bool
}

func RunTray(ctx context.Context, open, stop func() error) error {
	return runTray(ctx, open, stop, func(message uintptr, data *notifyIconData) bool {
		ok, _, _ := shellNotify.Call(message, uintptr(unsafe.Pointer(data)))
		return ok != 0
	})
}

func runTray(ctx context.Context, open, stop func() error, notify func(uintptr, *notifyIconData) bool) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var instance windows.Handle
	if err := windows.GetModuleHandleEx(0, nil, &instance); err != nil {
		return err
	}
	trayClassOnce.Do(func() {
		class := windowClass{Size: uint32(unsafe.Sizeof(windowClass{})), Proc: trayCallback, Instance: instance, Name: trayClassName}
		if atom, _, err := user32.NewProc("RegisterClassExW").Call(uintptr(unsafe.Pointer(&class))); atom == 0 {
			trayClassError = fmt.Errorf("register tray window: %w", err)
		}
	})
	if trayClassError != nil {
		return trayClassError
	}
	// A hidden top-level window receives TaskbarCreated broadcasts; a message-only
	// window would lose its icon permanently when Explorer restarts.
	hwnd, _, err := user32.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(trayClassName)), 0, 0, 0, 0, 0, 0, 0, 0, uintptr(instance), 0)
	if hwnd == 0 {
		return fmt.Errorf("create tray window: %w", err)
	}
	defer destroyWindow.Call(hwnd)
	taskbarCreated, _, err := user32.NewProc("RegisterWindowMessageW").Call(uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("TaskbarCreated"))))
	if taskbarCreated == 0 {
		return fmt.Errorf("register taskbar restart message: %w", err)
	}
	offset := binary.LittleEndian.Uint32(trayIcon[18:22])
	icon, _, err := user32.NewProc("CreateIconFromResourceEx").Call(uintptr(unsafe.Pointer(&trayIcon[offset])), uintptr(len(trayIcon))-uintptr(offset), 1, 0x00030000, 32, 32, 0)
	if icon == 0 {
		return fmt.Errorf("load tray icon: %w", err)
	}
	defer user32.NewProc("DestroyIcon").Call(icon)
	t := &trayWindow{hwnd: hwnd, taskbarCreated: uint32(taskbarCreated), open: open, stop: stop, notify: notify}
	t.icon = notifyIconData{Size: uint32(unsafe.Sizeof(notifyIconData{})), Window: windows.Handle(hwnd), ID: 1, Flags: 0x87, Callback: wmTray, Icon: windows.Handle(icon), Version: 4} // NIF_MESSAGE | NIF_ICON | NIF_TIP | NIF_SHOWTIP
	copy(t.icon.Tip[:], windows.StringToUTF16("Kilnbridge — background agent is running"))
	trayWindows.Store(hwnd, t)
	defer trayWindows.Delete(hwnd)
	defer t.notify(nimDelete, &t.icon)
	t.addIcon()
	// Explorer may not be ready at sign-in (or present in a headless CI session).
	// Retry until it accepts the icon, without preventing the worker from serving.
	if timer, _, err := user32.NewProc("SetTimer").Call(hwnd, 1, 2000, 0); timer == 0 {
		return fmt.Errorf("start tray retry timer: %w", err)
	}
	defer user32.NewProc("KillTimer").Call(hwnd, 1)
	finished := make(chan struct{})
	watcherDone := make(chan struct{})
	defer func() {
		close(finished)
		<-watcherDone
	}()
	go func() {
		defer close(watcherDone)
		select {
		case <-ctx.Done():
			postMessage.Call(hwnd, wmClose, 0, 0)
		case <-finished:
		}
	}()
	getMessage := user32.NewProc("GetMessageW")
	translate := user32.NewProc("TranslateMessage")
	dispatch := user32.NewProc("DispatchMessageW")
	var message windowMessage
	for {
		result, _, err := getMessage.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if int32(result) == -1 {
			return fmt.Errorf("read tray message: %w", err)
		}
		if result == 0 {
			return nil
		}
		translate.Call(uintptr(unsafe.Pointer(&message)))
		dispatch.Call(uintptr(unsafe.Pointer(&message)))
	}
}

func (t *trayWindow) addIcon() {
	t.added = t.notify(nimAdd, &t.icon)
	if t.added {
		t.notify(nimSetVersion, &t.icon)
	}
}

func trayWindowProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	if value, ok := trayWindows.Load(hwnd); ok {
		t := value.(*trayWindow)
		switch message {
		case t.taskbarCreated:
			t.addIcon()
			return 0
		case wmTimer:
			if !t.added {
				t.addIcon()
			}
			return 0
		case wmTray:
			switch uint16(lParam) {
			case ninSelect, ninKeySelect:
				t.action(trayOpen)
			case wmContextMenu:
				t.menu(int32(int16(wParam)), int32(int16(wParam>>16)))
			}
			return 0
		case wmClose:
			// Defer destruction until after the loop removes the notification icon.
			user32.NewProc("PostQuitMessage").Call(0)
			return 0
		}
	}
	result, _, _ := defWindowProc.Call(hwnd, uintptr(message), wParam, lParam)
	return result
}

func (t *trayWindow) action(command uintptr) {
	var err error
	switch command {
	case trayOpen:
		// Transfer foreground activation to the settings process we launch.
		setForeground.Call(t.hwnd)
		err = t.open()
	case trayStop:
		err = t.stop()
	}
	if err != nil {
		Alert(err.Error())
	}
}

func (t *trayWindow) menu(x, y int32) {
	menu, _, err := user32.NewProc("CreatePopupMenu").Call()
	if menu == 0 {
		Alert(fmt.Sprintf("Could not open the tray menu: %v", err))
		return
	}
	defer user32.NewProc("DestroyMenu").Call(menu)
	appendMenu := user32.NewProc("AppendMenuW")
	appendMenu.Call(menu, 0x0002, 0, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("Background agent is running")))) // MF_DISABLED
	appendMenu.Call(menu, 0x0800, 0, 0)                                                                                // MF_SEPARATOR
	appendMenu.Call(menu, 0, trayOpen, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("Open Kilnbridge"))))
	appendMenu.Call(menu, 0, trayStop, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("Stop background agent"))))
	user32.NewProc("SetMenuDefaultItem").Call(menu, trayOpen, 0)
	setForeground.Call(t.hwnd)
	command, _, _ := user32.NewProc("TrackPopupMenu").Call(menu, 0x0102, uintptr(x), uintptr(y), 0, t.hwnd, 0) // TPM_RETURNCMD | TPM_RIGHTBUTTON
	// Required for reliable dismissal when clicking outside the notification menu.
	postMessage.Call(t.hwnd, 0, 0, 0)
	if command != 0 {
		t.action(command)
	} else {
		t.notify(nimSetFocus, &t.icon)
	}
}
