//go:build windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                   = windows.NewLazySystemDLL("user32.dll")
	procPostMessageW         = user32.NewProc("PostMessageW")
	procGetWindowTextW       = user32.NewProc("GetWindowTextW")
	procFlashWindowEx        = user32.NewProc("FlashWindowEx")
	procSendMessageW         = user32.NewProc("SendMessageW")
	procGetSystemMetrics     = user32.NewProc("GetSystemMetrics")
	procPrivateExtractIconsW = user32.NewProc("PrivateExtractIconsW")
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGetConsoleWindow     = kernel32.NewProc("GetConsoleWindow")
	procSetConsoleTitleW     = kernel32.NewProc("SetConsoleTitleW")
	procSetConsoleIcon       = kernel32.NewProc("SetConsoleIcon")
	procReadConsoleInputW    = kernel32.NewProc("ReadConsoleInputW")
)

const (
	wmClose   = 0x0010
	dialogCls = "#32770"
)

// winInfo is one top-level window of a process.
type winInfo struct {
	HWND    windows.HWND
	Title   string
	Class   string
	Visible bool
}

// windowsOf enumerates the top-level windows owned by pid.
func windowsOf(pid int) []winInfo {
	var out []winInfo
	cb := syscall.NewCallback(func(h windows.HWND, _ uintptr) uintptr {
		var owner uint32
		if _, err := windows.GetWindowThreadProcessId(h, &owner); err != nil || int(owner) != pid {
			return 1
		}
		w := winInfo{HWND: h, Visible: windows.IsWindowVisible(h)}
		buf := make([]uint16, 256)
		n, _, _ := procGetWindowTextW.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), 256)
		w.Title = windows.UTF16ToString(buf[:n])
		cls := make([]uint16, 64)
		if c, err := windows.GetClassName(h, &cls[0], 64); err == nil {
			w.Class = windows.UTF16ToString(cls[:c])
		}
		out = append(out, w)
		return 1
	})
	_ = windows.EnumWindows(cb, nil)
	return out
}

// winApp implements AppControl. RequestClose posts WM_CLOSE to the pid's
// own top-level windows (what `taskkill /PID` without /F does); nothing
// else is ever sent to other processes' windows.
type winApp struct {
	procs winProcs
	start func(argv []string) error
}

var _ AppControl = (*winApp)(nil)

func newWinApp(p winProcs) *winApp { return &winApp{procs: p, start: startDetached} }

func (a *winApp) IsRunning(pid int) bool { return a.procs.Alive(pid) }

// RequestClose asks the app to close so it can save its state. It fails
// when pid has no top-level window (a hidden/tray-only or console app), in
// which case the caller waits and then forces.
func (a *winApp) RequestClose(_ context.Context, pid int) error {
	if pid <= 0 || pid == os.Getpid() {
		return fmt.Errorf("close: refusing pid %d", pid)
	}
	wins := windowsOf(pid)
	if len(wins) == 0 {
		return fmt.Errorf("close pid %d: no window to close", pid)
	}
	var errs []error
	for _, w := range wins {
		if r, _, err := procPostMessageW.Call(uintptr(w.HWND), wmClose, 0, 0); r == 0 {
			errs = append(errs, fmt.Errorf("post WM_CLOSE: %w", err))
		}
	}
	if len(errs) == len(wins) {
		return fmt.Errorf("close pid %d: %w", pid, errors.Join(errs...))
	}
	return nil
}

// ForceClose terminates p and its children (`taskkill /T /F`).
func (a *winApp) ForceClose(ctx context.Context, p Process) error { return a.procs.KillTree(ctx, p) }

// CloseBlocker is the read-only diagnosis of python-behaviour §2.7 step 5.
func (a *winApp) CloseBlocker(pid int) string {
	var vis []winInfo
	for _, w := range windowsOf(pid) {
		if w.Visible {
			vis = append(vis, w)
		}
	}
	return blockerText(vis)
}

func blockerText(wins []winInfo) string {
	if len(wins) == 0 {
		return "no visible window left: the app was likely still shutting down its backend"
	}
	var parts []string
	dialog := false
	for i, w := range wins {
		dialog = dialog || w.Class == dialogCls
		if i < 4 {
			parts = append(parts, fmt.Sprintf("%s [%s]", w.Title, w.Class))
		}
	}
	if dialog {
		return "a dialog was open, most likely the 'still working' quit confirmation: " + strings.Join(parts, "; ")
	}
	return "windows still open: " + strings.Join(parts, "; ")
}

// CanLaunch: the exe must exist (explorer.exe shows a modal error dialog
// for a missing path).
func (a *winApp) CanLaunch(exe string) (bool, string) {
	if st, err := os.Stat(exe); err != nil || st.IsDir() {
		return false, "desktop app not found: " + exe
	}
	return true, ""
}

// Launch starts the app through explorer.exe, as the Python updater did:
// a normal, un-elevated process that is not a child of this console.
func (a *winApp) Launch(_ context.Context, exe string, args []string) error {
	if ok, why := a.CanLaunch(exe); !ok {
		return errors.New(why)
	}
	if len(args) > 0 {
		return errors.New("launch: arguments are not supported through explorer.exe")
	}
	explorer := "explorer.exe"
	if dir, err := windows.GetWindowsDirectory(); err == nil {
		explorer = filepath.Join(dir, "explorer.exe")
	}
	return a.start([]string{explorer, exe})
}

func startDetached(argv []string) error {
	c := exec.Command(argv[0], argv[1:]...)
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	if err := c.Start(); err != nil {
		return fmt.Errorf("launch %s: %w", argv[len(argv)-1], err)
	}
	// explorer.exe hands off and exits; reap it in the background.
	go func() { _ = c.Wait() }()
	return nil
}
