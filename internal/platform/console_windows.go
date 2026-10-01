//go:build windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unsafe"

	"golang.org/x/sys/windows"
)

// winConsole implements Console on the Windows console API. ReadKey uses
// ReadConsoleInput, so Ctrl+C arrives as a key (D3: only listed keys count;
// arrows/function keys are reported as Other and ignored by prompts).
type winConsole struct {
	out, in windows.Handle
	getenv  func(string) string
	hwnd    func() windows.HWND
	self    string // own executable, for the identity child

	mu       sync.Mutex
	keyCh    chan Key
	readerOn bool
}

var _ Console = (*winConsole)(nil)

func newWinConsole() *winConsole {
	exe, _ := os.Executable()
	return &winConsole{
		out:    windows.Handle(os.Stdout.Fd()),
		in:     windows.Handle(os.Stdin.Fd()),
		getenv: os.Getenv,
		hwnd:   consoleHWND,
		self:   exe,
	}
}

func consoleHWND() windows.HWND {
	h, _, _ := procGetConsoleWindow.Call()
	return windows.HWND(h)
}

func (c *winConsole) IsTerminal() bool {
	var m uint32
	return windows.GetConsoleMode(c.out, &m) == nil
}

// EnableVT turns on VT processing; repeated calls are fine (children reset
// the mode). It fails on a pipe/file so the caller picks plain output.
func (c *winConsole) EnableVT() (func(), error) {
	var orig uint32
	if err := windows.GetConsoleMode(c.out, &orig); err != nil {
		return func() {}, fmt.Errorf("not a console: %w", err)
	}
	want := orig | windows.ENABLE_PROCESSED_OUTPUT | windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING
	if err := windows.SetConsoleMode(c.out, want); err != nil {
		return func() {}, fmt.Errorf("enable VT: %w", err)
	}
	return func() { _ = windows.SetConsoleMode(c.out, orig) }, nil
}

func (c *winConsole) Size() (int, int, bool) {
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(c.out, &info); err != nil {
		return 0, 0, false
	}
	cols := int(info.Window.Right-info.Window.Left) + 1
	rows := int(info.Window.Bottom-info.Window.Top) + 1
	return cols, rows, cols > 0
}

func (c *winConsole) SetTitle(title string) error {
	p, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return err
	}
	if r, _, e := procSetConsoleTitleW.Call(uintptr(unsafe.Pointer(p))); r == 0 {
		return e
	}
	return nil
}

func (c *winConsole) IsForeground() bool {
	h := c.window()
	return h == 0 || windows.GetForegroundWindow() == h
}

func (c *winConsole) window() windows.HWND {
	if c.hwnd == nil {
		return 0
	}
	return c.hwnd()
}

type flashInfo struct {
	size    uint32
	hwnd    windows.HWND
	flags   uint32
	count   uint32
	timeout uint32
}

// Flash flashes the taskbar button until the window is focused, only when
// it is not already in the foreground.
func (c *winConsole) Flash() {
	h := c.window()
	if h == 0 || c.IsForeground() {
		return
	}
	fi := flashInfo{hwnd: h, flags: 0x2 | 0xC} // FLASHW_TRAY | FLASHW_TIMERNOFG
	fi.size = uint32(unsafe.Sizeof(fi))
	_, _, _ = procFlashWindowEx.Call(uintptr(unsafe.Pointer(&fi)))
}

// ---- keys ----

// keyFromEvent maps one KEY_EVENT to a Key; ok=false for events to skip
// (key-up, bare modifiers).
func keyFromEvent(down bool, vk, ch uint16) (Key, bool) {
	if !down {
		return Key{}, false
	}
	switch vk {
	case 0x10, 0x11, 0x12, 0x14, 0x5B, 0x5C, 0x90, 0x91: // shift, ctrl, alt, caps, win, num/scroll lock
		return Key{}, false
	}
	switch {
	case ch == 0x03:
		return Key{CtrlC: true}, true
	case ch == 0:
		return Key{Other: true}, true
	}
	return Key{Rune: unicode.ToLower(rune(ch))}, true
}

type keyEventRecord struct {
	eventType uint16
	_         uint16
	keyDown   int32
	repeat    uint16
	vk        uint16
	scan      uint16
	char      uint16
	ctrl      uint32
}

// startReader runs one goroutine that blocks on console input; keys are
// handed out through keyCh (a blocking console read cannot be cancelled).
func (c *winConsole) startReader() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readerOn {
		return nil
	}
	var m uint32
	if err := windows.GetConsoleMode(c.in, &m); err != nil {
		return fmt.Errorf("no console input: %w", err)
	}
	// raw-ish: no line input/echo, Ctrl+C as a key
	_ = windows.SetConsoleMode(c.in, m&^(windows.ENABLE_LINE_INPUT|windows.ENABLE_ECHO_INPUT|windows.ENABLE_PROCESSED_INPUT))
	c.keyCh = make(chan Key, 16)
	c.readerOn = true
	go func() {
		var rec [1]keyEventRecord
		for {
			var n uint32
			r, _, _ := procReadConsoleInputW.Call(uintptr(c.in), uintptr(unsafe.Pointer(&rec[0])), 1, uintptr(unsafe.Pointer(&n)))
			if r == 0 {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			if n == 0 || rec[0].eventType != 0x0001 { // KEY_EVENT
				continue
			}
			if k, ok := keyFromEvent(rec[0].keyDown != 0, rec[0].vk, rec[0].char); ok {
				select {
				case c.keyCh <- k:
				default: // nobody is asking; drop
				}
			}
		}
	}()
	return nil
}

// ReadKey waits for one key until ctx is done. Keys pressed before the
// prompt was shown are discarded (they answered nothing).
func (c *winConsole) ReadKey(ctx context.Context) (Key, error) {
	if err := c.startReader(); err != nil {
		<-ctx.Done()
		return Key{}, ctx.Err()
	}
	select {
	case k := <-c.keyCh:
		return k, nil
	case <-ctx.Done():
		return Key{}, ctx.Err()
	}
}

// ---- identity (§6.3) ----

// SetIdentity gives the console window the Hermes icon and its own taskbar
// group. The AppUserModelID is set by a short-lived child (this exe with
// the hidden --appid flag) so a COM fault can never take the updater down.
func (c *winConsole) SetIdentity(appID string, iconCandidates []string) string {
	if c.getenv != nil && c.getenv("WT_SESSION") != "" {
		return "skipped: windows terminal"
	}
	h := c.window()
	if h == 0 {
		return "skipped: no console window"
	}
	status := "icon: none found"
	if setConsoleIcon(h, iconCandidates) {
		status = "icon: set"
	}
	if c.self != "" && appID != "" {
		cmd := exec.Command(c.self, "--appid", fmt.Sprintf("%d", uintptr(h)))
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
		if err := cmd.Start(); err == nil {
			go func() { _ = cmd.Wait() }()
			status += ", app id: requested"
		} else {
			status += ", app id: " + err.Error()
		}
	}
	return status
}

var keptIcons []uintptr // never destroyed: the window uses them until exit

func setConsoleIcon(h windows.HWND, candidates []string) bool {
	small, _, _ := procGetSystemMetrics.Call(49) // SM_CXSMICON
	big, _, _ := procGetSystemMetrics.Call(11)   // SM_CXICON
	for _, path := range candidates {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		p, err := windows.UTF16PtrFromString(path)
		if err != nil {
			continue
		}
		icons := [2]uintptr{}
		for i, size := range []uintptr{small, big} {
			var hicon, id uintptr
			n, _, _ := procPrivateExtractIconsW.Call(uintptr(unsafe.Pointer(p)), 0, size, size,
				uintptr(unsafe.Pointer(&hicon)), uintptr(unsafe.Pointer(&id)), 1, 0)
			if n == 0 || n == 0xFFFFFFFF || hicon == 0 {
				break
			}
			icons[i] = hicon
		}
		if icons[0] == 0 || icons[1] == 0 {
			continue
		}
		keptIcons = append(keptIcons, icons[0], icons[1])
		if procSetConsoleIcon.Find() == nil {
			_, _, _ = procSetConsoleIcon.Call(icons[1])
		}
		_, _, _ = procSendMessageW.Call(uintptr(h), 0x80, 0, icons[0]) // WM_SETICON, ICON_SMALL
		_, _, _ = procSendMessageW.Call(uintptr(h), 0x80, 1, icons[1]) // ICON_BIG
		return true
	}
	return false
}

// ---- progress ----

// wtProgress writes Windows Terminal's OSC 9;4 progress sequences. The
// percentage is truncated like the Python (int(frac*100)).
type wtProgress struct {
	mu sync.Mutex
	w  interface{ WriteString(string) (int, error) }
}

func (p *wtProgress) Set(s ProgressState, f float64) {
	pct := math.Floor(f*100) / 100
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = p.w.WriteString(oscProgress(s, pct)) // whole percent: the shared builder's +0.5 rounding cannot raise it
}

func (p *wtProgress) Close() { p.Set(ProgressNone, 0) }

// ---- disk ----

type winDisk struct{ getenv func(string) string }

var _ Disk = winDisk{}

// Free reports the bytes available to the user on path's volume; a missing
// path is measured at its nearest existing parent.
func (winDisk) Free(path string) (uint64, error) {
	for p := path; p != ""; {
		if _, err := os.Stat(p); err == nil {
			ptr, err := windows.UTF16PtrFromString(p)
			if err != nil {
				return 0, err
			}
			var free, total, totalFree uint64
			if err := windows.GetDiskFreeSpaceEx(ptr, &free, &total, &totalFree); err != nil {
				return 0, fmt.Errorf("free space of %q: %w", p, err)
			}
			return free, nil
		}
		parent := parentDir(p)
		if parent == p {
			break
		}
		p = parent
	}
	return 0, fmt.Errorf("free space of %q: no existing parent", path)
}

func parentDir(p string) string {
	p = strings.TrimRight(p, `\/`)
	i := strings.LastIndexAny(p, `\/`)
	if i <= 0 {
		return p
	}
	if i == 2 && p[1] == ':' {
		return p[:3]
	}
	return p[:i]
}

// Volumes lists drive letters with their type; System = %SystemDrive%.
func (d winDisk) Volumes() ([]Volume, error) {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil, fmt.Errorf("list drives: %w", err)
	}
	sys := "C:"
	if d.getenv != nil {
		if s := strings.TrimSpace(d.getenv("SystemDrive")); s != "" {
			sys = s
		}
	}
	var out []Volume
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		p, _ := windows.UTF16PtrFromString(root)
		typ := windows.GetDriveType(p)
		if typ == windows.DRIVE_NO_ROOT_DIR || typ == windows.DRIVE_UNKNOWN {
			continue
		}
		v := Volume{Root: root, Fixed: typ == windows.DRIVE_FIXED, System: strings.EqualFold(root[:2], sys)}
		var free, total, totalFree uint64
		if windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree) == nil {
			v.Free = free
		}
		out = append(out, v)
	}
	return out, nil
}

// ---- autostart (logon tasks) ----

type winAutostart struct{}

var _ Autostart = winAutostart{}

func (winAutostart) Supported() bool { return true }

// TaskExecutable reads a scheduled task's XML: found=false when the task
// does not exist (schtasks exits non-zero).
func (winAutostart) TaskExecutable(ctx context.Context, name string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	sys, _ := windows.GetSystemDirectory()
	cmd := exec.CommandContext(ctx, sys+`\schtasks.exe`, "/Query", "/TN", name, "/XML")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("schtasks: %w", err)
	}
	return taskExe(decodeSchtasks(out)), true, nil
}

// decodeSchtasks handles UTF-16LE (with or without BOM) and UTF-8 output.
func decodeSchtasks(b []byte) string {
	utf16le := len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE
	if !utf16le {
		for i := 0; i < len(b) && i < 200; i++ {
			if b[i] == 0 {
				utf16le = true
				break
			}
		}
	}
	if !utf16le {
		return string(b)
	}
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		b = b[2:]
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return windows.UTF16ToString(u)
}

// taskExeRe finds the cua-driver path anywhere in the task XML, exactly as
// the Python does (python-behaviour 2.11): the real task runs powershell.exe
// and names the driver inside <Arguments> ("Start-Process -FilePath '...'").
var taskExeRe = regexp.MustCompile(`(?i)([A-Za-z]:\\[^'"<>]*?cua-driver\.exe)`)

// taskExe returns the cua-driver.exe path named anywhere in the task XML, or
// "" when the task names none.
func taskExe(xml string) string {
	if m := taskExeRe.FindStringSubmatch(xml); m != nil {
		return m[1]
	}
	return ""
}

type shellExecuteInfo struct {
	size      uint32
	mask      uint32
	hwnd      windows.Handle
	verb      *uint16
	file      *uint16
	params    *uint16
	dir       *uint16
	show      int32
	instApp   windows.Handle
	idList    uintptr
	class     *uint16
	keyClass  windows.Handle
	hotKey    uint32
	iconOrMon windows.Handle
	hProcess  windows.Handle
}

var (
	shell32             = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteExW = shell32.NewProc("ShellExecuteExW")
)

// elevatedParams quotes argv[1:]-style arguments with the Windows rules
// (D16); no shell string is built from them.
func elevatedParams(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = windows.EscapeArg(a)
	}
	return strings.Join(q, " ")
}

// RunElevated runs argv once with the "runas" verb (one UAC prompt) and
// waits for it. Output is not captured (an elevated child cannot write to
// our pipes); the exit code is.
func (winAutostart) RunElevated(ctx context.Context, argv []string) (int, string, error) {
	if len(argv) == 0 {
		return -1, "", errors.New("run elevated: empty argv")
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(argv[0])
	if err != nil {
		return -1, "", err
	}
	params, err := windows.UTF16PtrFromString(elevatedParams(argv[1:]))
	if err != nil {
		return -1, "", err
	}
	sei := shellExecuteInfo{mask: 0x00000040 | 0x00000400, verb: verb, file: file, params: params, show: 0} // NOCLOSEPROCESS | FLAG_NO_UI off; SW_HIDE
	sei.size = uint32(unsafe.Sizeof(sei))
	if r, _, e := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&sei))); r == 0 {
		if errors.Is(e, windows.ERROR_CANCELLED) {
			return -1, "", fmt.Errorf("administrator approval was declined")
		}
		return -1, "", fmt.Errorf("run elevated: %w", e)
	}
	if sei.hProcess == 0 {
		return -1, "", errors.New("run elevated: no process handle")
	}
	defer windows.CloseHandle(sei.hProcess)
	for {
		ev, err := windows.WaitForSingleObject(sei.hProcess, 250)
		if err != nil {
			return -1, "", err
		}
		if ev == windows.WAIT_OBJECT_0 {
			break
		}
		if ctx.Err() != nil {
			return -1, "", ctx.Err()
		}
	}
	var code uint32
	if err := windows.GetExitCodeProcess(sei.hProcess, &code); err != nil {
		return -1, "", err
	}
	return int(code), "", nil
}
