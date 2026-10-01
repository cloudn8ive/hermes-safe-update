// Package platform hides everything operating-system specific behind small
// interfaces: where Hermes lives, how to list/close/launch processes, the
// console, taskbar progress, logon tasks and disk space. Business logic
// never checks runtime.GOOS; it receives a *Platform from New() (or the fakes
// in fake.go in tests).
//
// File ownership: platform.go and fake.go belong to the architect (T1);
// *_windows.go to I1b; *_darwin.go, *_linux.go and *_unix.go to I5.
package platform

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrNotSupported is returned by operations that do not exist on this OS
// (for example logon tasks on macOS). Callers show an info row, not an error.
var ErrNotSupported = errors.New("not supported on this platform")

// ErrNotImplemented marks skeleton stubs that an implementer still has to
// fill in. It must not survive into a release (CI greps for it in cmd/).
var ErrNotImplemented = errors.New("not implemented yet")

// ErrProcessChanged means a pid no longer belongs to the process that was
// classified (it exited and the pid was reused), so nothing was terminated.
var ErrProcessChanged = errors.New("process changed since it was classified")

// createdTolerance absorbs the rounding of start times derived from /proc
// ticks; real pid reuse differs by far more.
const createdTolerance = time.Second

// sameCreated reports whether two start times are the same process start.
func sameCreated(a, b time.Time) bool {
	d := a.Sub(b)
	return d > -createdTolerance && d < createdTolerance
}

// CheckIdentity verifies that want.PID in the fresh snapshot all is still
// the process that was classified. It returns an error wrapping
// ErrProcessChanged when the pid is missing or its Created differs. A zero
// want.Created means no identity is known and nothing is checked.
func CheckIdentity(all []Process, want Process) error {
	if want.Created.IsZero() {
		return nil
	}
	for _, p := range all {
		if p.PID != want.PID {
			continue
		}
		if p.Created.IsZero() || !sameCreated(p.Created, want.Created) {
			return fmt.Errorf("pid %d: started %s, now %s: %w", want.PID, want.Created.UTC().Format(time.RFC3339), createdText(p.Created), ErrProcessChanged)
		}
		return nil
	}
	return fmt.Errorf("pid %d is no longer running: %w", want.PID, ErrProcessChanged)
}

func createdText(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.UTC().Format(time.RFC3339)
}

// Platform bundles the per-OS backends.
type Platform struct {
	OS     string // "windows", "darwin", "linux"
	Tested bool   // true only where behaviour was run on a real machine (Windows)

	Paths     Paths
	App       AppControl
	Procs     Procs
	Console   Console
	Progress  Progress
	Autostart Autostart
	Disk      Disk
}

// Env is what path resolution may look at. Tests pass a fake getenv.
type Env struct {
	Getenv   func(string) string
	UserHome string // os.UserHomeDir(); may be empty
}

// Paths resolves default locations. Methods return candidates or plain
// paths; whether something exists is checked by internal/hermes.
type Paths interface {
	// HermesHome is the Hermes data root: %LOCALAPPDATA%\hermes on Windows,
	// ~/.hermes elsewhere, honouring HERMES_DATA_DIR_SUFFIX.
	HermesHome(env Env) (string, error)
	// UserData is the desktop app's Electron userData dir (stable variant),
	// honouring HERMES_DESKTOP_USER_DATA_DIR and HERMES_DATA_DIR_SUFFIX.
	UserData(env Env) (string, error)
	// LauncherCandidates lists `hermes` CLI launchers in preference order.
	LauncherCandidates(env Env, home, checkout string) []string
	// DesktopAppCandidates lists the source-built desktop executable(s) under
	// <checkout>/apps/desktop/release in preference order.
	DesktopAppCandidates(checkout string) []string
	// DesktopProcessMarkers are lower-case substrings of the desktop main
	// process's command line ("win-unpacked", "/contents/macos/hermes", ...).
	DesktopProcessMarkers() []string
	// ManagedPythonGlobs are glob patterns for Hermes' bundled Python
	// interpreter (last sorted match wins).
	ManagedPythonGlobs(home string) []string
	// BundledGitGlobs are glob patterns for Hermes' bundled git (D4: bundled
	// first, then PATH).
	BundledGitGlobs(home string) []string
	// ElectronLeaf is the Electron executable relative to
	// node_modules/electron/dist when path.txt is missing.
	ElectronLeaf() string
	// PackagedInstallHints lists paths whose existence means Hermes was
	// installed as a package (macOS Hermes.app, MSIX, ...) rather than as a
	// managed source checkout. See internal/hermes install-kind detection.
	PackagedInstallHints(env Env) []PackagedHint
}

// PackagedHint is one place a packaged (non-checkout) install can live.
type PackagedHint struct {
	Kind string // "macos-app", "msix", "appimage", "docker", "nix"
	Path string
	Use  string // what to use instead, shown to the user
}

// Process is one entry of a process snapshot.
type Process struct {
	PID     int
	PPID    int
	Name    string // executable base name, as the OS reports it
	Exe     string // full executable path if known
	Cmdline string // full command line if readable, else ""
	Created time.Time
}

// Procs lists and stops processes. Implementations use native APIs
// (D15: Toolhelp snapshot + PEB command line on Windows, /proc on Linux,
// sysctl/ps on macOS); no PowerShell/WMI.
type Procs interface {
	List(ctx context.Context) ([]Process, error)
	Alive(pid int) bool
	// KillTree force-stops p and all its descendants. p is the process as it
	// was classified: when p.Created is set, KillTree looks p.PID up in a
	// fresh snapshot and refuses with ErrProcessChanged if the pid is gone
	// or now belongs to a process with another start time (pid reuse). A
	// zero Created skips that check (our own just-started child).
	KillTree(ctx context.Context, p Process) error
}

// AppControl closes and relaunches the Hermes desktop app.
type AppControl interface {
	// IsRunning reports whether pid is still alive.
	IsRunning(pid int) bool
	// RequestClose asks the app to quit the way a user would (WM_CLOSE /
	// Cmd+Q / SIGTERM) so it can save state. It does not wait.
	RequestClose(ctx context.Context, pid int) error
	// ForceClose stops p and its children immediately, with the same
	// identity check as Procs.KillTree.
	ForceClose(ctx context.Context, p Process) error
	// CloseBlocker returns a one-line read-only diagnosis of why pid did not
	// close (e.g. "a dialog was open: ..."); "" if unknown. Never fails.
	CloseBlocker(pid int) string
	// Launch starts the desktop app detached, as the normal (non-elevated)
	// user, not tied to this console.
	Launch(ctx context.Context, exe string, args []string) error
	// CanLaunch reports whether launching a GUI app makes sense here
	// (Linux: a display and a usable sandbox; SSH sessions: no).
	CanLaunch(exe string) (bool, string)
}

// Key is one key press read from the console.
type Key struct {
	Rune  rune // lower-cased printable key, '\r' for Enter, 0x1b for Esc
	CtrlC bool // Ctrl+C read as a key (raw mode), treat as abort
	Other bool // arrow/function/other non-character key (D3: ignored by prompts)
}

// Console is the terminal the tool runs in.
type Console interface {
	// IsTerminal reports whether stdout is a real console (not a pipe/file).
	IsTerminal() bool
	// EnableVT turns on ANSI/VT processing (Windows) and returns a restore
	// func. Must be safe to call repeatedly (children reset the mode).
	EnableVT() (restore func(), err error)
	// Size returns the console width and height in cells.
	Size() (cols, rows int, ok bool)
	SetTitle(title string) error
	// SetIdentity gives the console window its own icon and taskbar group
	// (Windows AppUserModelID); returns a short status string; never fails.
	SetIdentity(appID string, iconCandidates []string) string
	// IsForeground reports whether the console window has focus.
	IsForeground() bool
	// Flash asks for attention (taskbar flash) when not in the foreground.
	Flash()
	// ReadKey waits for one key until ctx is done (ctx error returned).
	ReadKey(ctx context.Context) (Key, error)
}

// ProgressState mirrors the Windows taskbar states / OSC 9;4 states.
type ProgressState int

const (
	ProgressNone ProgressState = iota
	ProgressNormal
	ProgressIndeterminate
	ProgressError
	ProgressPaused
)

// Progress shows run progress outside the text (taskbar, terminal tab).
type Progress interface {
	Set(state ProgressState, fraction float64)
	// Close leaves the final state visible and releases resources.
	Close()
}

// Autostart covers logon tasks such as cua-driver-serve (Windows only).
type Autostart interface {
	Supported() bool
	// TaskExecutable returns the executable a named logon task runs.
	// found=false means no such task (on-demand mode); exe may be "" if the
	// task exists but names no recognisable executable.
	TaskExecutable(ctx context.Context, name string) (exe string, found bool, err error)
	// RunElevated runs argv once with administrator rights (one UAC prompt
	// on Windows) and waits. Argv is quoted by the implementation (D16).
	RunElevated(ctx context.Context, argv []string) (exitCode int, output string, err error)
}

// Volume is one mounted filesystem.
type Volume struct {
	Root   string // "D:\\" or "/"
	Free   uint64
	Fixed  bool // a local fixed disk (not removable/network)
	System bool // holds the OS
}

// Disk reports free space.
type Disk interface {
	Free(path string) (uint64, error)
	Volumes() ([]Volume, error)
}
