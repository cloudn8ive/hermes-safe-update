package platform

// macOS + Linux app control and process-tree logic, written against small
// injected seams (signaller, runFn) so the quit/force/kill decisions can be
// unit-tested on any OS. The real seams live in unixreal_unix.go.
// Nothing here was run against a real macOS or Linux machine.

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

// hermesBundleID is the macOS bundle id of the stable Hermes desktop app
// (appId in product-identity.cjs).
const hermesBundleID = "com.nousresearch.hermes"

// signaller sends the two signals the updater needs. The real one wraps
// kill(2); fakes record calls.
type signaller interface {
	Term(pid int) error
	Kill(pid int) error
	// Exists reports whether pid is a live process (signal 0; EPERM counts
	// as alive).
	Exists(pid int) bool
}

// runFn runs a short external command and returns its combined output.
type runFn func(ctx context.Context, name string, args ...string) (string, error)

// descendants returns every descendant of root in procs, deepest first (the
// order to kill them in so parents cannot respawn children), root excluded.
func descendants(procs []Process, root int) []int {
	byPID := map[int]Process{}
	kids := map[int][]int{}
	for _, p := range procs {
		byPID[p.PID] = p
		if p.PID != p.PPID {
			kids[p.PPID] = append(kids[p.PPID], p.PID)
		}
	}
	type item struct{ pid, depth int }
	var all []item
	seen := map[int]bool{root: true}
	queue := []item{{root, 0}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		ks := kids[cur.pid]
		sort.Ints(ks)
		for _, k := range ks {
			if seen[k] {
				continue
			}
			// stale ppid: the real parent died and its pid was reused
			if pc, kc := byPID[cur.pid].Created, byPID[k].Created; !pc.IsZero() && !kc.IsZero() && kc.Before(pc) {
				continue
			}
			seen[k] = true
			all = append(all, item{k, cur.depth + 1})
			queue = append(queue, item{k, cur.depth + 1})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].depth > all[j].depth })
	out := make([]int, len(all))
	for i, it := range all {
		out[i] = it.pid
	}
	return out
}

// ancestors returns pid's parent chain (nearest first) according to procs.
func ancestors(procs []Process, pid int) []int {
	parent := map[int]int{}
	for _, p := range procs {
		parent[p.PID] = p.PPID
	}
	var out []int
	for cur, n := parent[pid], 0; cur > 0 && n < 4096; cur, n = parent[cur], n+1 {
		out = append(out, cur)
	}
	return out
}

// unixProcs is the process table plus tree kill, shared by both OSes.
type unixProcs struct {
	list     func(ctx context.Context) ([]Process, error)
	sig      signaller
	self     int            // our own pid; never killed
	isZombie func(int) bool // optional (Linux); a zombie is not alive
}

var _ Procs = (*unixProcs)(nil)

// List returns the current process table. Untested on a real macOS or Linux
// machine.
func (u *unixProcs) List(ctx context.Context) ([]Process, error) { return u.list(ctx) }

// Alive reports whether pid is running (a Linux zombie is not). Untested on
// a real macOS or Linux machine.
func (u *unixProcs) Alive(pid int) bool {
	if pid <= 0 || !u.sig.Exists(pid) {
		return false
	}
	return u.isZombie == nil || !u.isZombie(pid)
}

// Process start times (Created) come from /proc on Linux; macOS does not
// report them (Created is always zero), so the reused-pid check in KillTree
// and the stale-ppid check in descendants() do not apply there.
//
// KillTree sends SIGKILL to every descendant of pid (deepest first) and then
// to pid. It refuses to touch our own process or any of our ancestors, and
// ignores processes that already exited. Untested on a real macOS or Linux
// machine.
func (u *unixProcs) KillTree(ctx context.Context, p Process) error {
	pid := p.PID
	if pid <= 1 {
		return fmt.Errorf("refusing to kill pid %d", pid)
	}
	procs, err := u.list(ctx)
	if err != nil {
		return fmt.Errorf("list processes before killing %d: %w", pid, err)
	}
	if err := CheckIdentity(procs, p); err != nil {
		return fmt.Errorf("refusing to kill pid %d: %w", pid, err)
	}
	victims := append(descendants(procs, pid), pid)
	protected := map[int]bool{u.self: true}
	for _, a := range ancestors(procs, u.self) {
		protected[a] = true
	}
	for _, v := range victims {
		if protected[v] {
			return fmt.Errorf("refusing to kill pid %d: it is this tool or one of its parents", v)
		}
	}
	var errs []error
	for _, v := range victims {
		if err := u.sig.Kill(v); err != nil && u.sig.Exists(v) {
			errs = append(errs, fmt.Errorf("kill %d: %w", v, err))
		}
	}
	return errors.Join(errs...)
}

// unixApp implements AppControl for macOS and Linux.
type unixApp struct {
	goos     string
	sig      signaller
	procs    Procs
	run      runFn
	getenv   func(string) string
	exists   func(string) bool
	launcher func(ctx context.Context, exe string, args []string) error // detached start, Linux

	chromeSandboxSetuid func(path string) bool // root-owned setuid file (Linux)
	userNamespacesWork  func() bool            // `unshare --user --map-root-user true` (Linux)
}

var _ AppControl = (*unixApp)(nil)

// IsRunning reports whether pid is still alive. Untested on a real macOS or
// Linux machine.
func (a *unixApp) IsRunning(pid int) bool { return a.procs.Alive(pid) }

// RequestClose asks the app to quit without waiting. macOS: a normal Cmd+Q
// through AppleScript addressed by bundle id (this reaches every copy with
// that id, and falls back to SIGTERM to pid if osascript fails). Linux:
// SIGTERM to the main pid only, never the process group. On macOS, if pid is
// already gone this returns nil without running osascript, because
// `tell application ... to quit` launches an app that is not running.
// osascript can also block on the macOS Automation (TCC) permission prompt
// until the runner timeout (30 s) expires; the SIGTERM fallback then applies.
// Untested on a real macOS or Linux machine.
func (a *unixApp) RequestClose(ctx context.Context, pid int) error {
	if a.goos == "darwin" {
		if !a.procs.Alive(pid) {
			return nil
		}
		_, err := a.run(ctx, "osascript", "-e", `tell application id "`+hermesBundleID+`" to quit`)
		if err == nil {
			return nil
		}
		if termErr := a.sig.Term(pid); termErr != nil {
			return fmt.Errorf("osascript quit failed (%v) and SIGTERM failed: %w", err, termErr)
		}
		return nil
	}
	if err := a.sig.Term(pid); err != nil {
		return fmt.Errorf("SIGTERM %d: %w", pid, err)
	}
	return nil
}

// ForceClose SIGKILLs pid and its descendants. Untested on a real macOS or
// Linux machine.
func (a *unixApp) ForceClose(ctx context.Context, p Process) error { return a.procs.KillTree(ctx, p) }

// CloseBlocker has no read-only diagnosis on macOS/Linux and returns "".
// Untested on a real macOS or Linux machine.
func (a *unixApp) CloseBlocker(int) string { return "" }

// bundlePath maps ".../Hermes.app/Contents/MacOS/Hermes" to ".../Hermes.app";
// "" when exe is not inside a bundle.
func bundlePath(exe string) string {
	i := strings.Index(exe, ".app/Contents/MacOS/")
	if i < 0 {
		return ""
	}
	return exe[:i+len(".app")]
}

// Launch starts the app detached. macOS: clear the quarantine flag
// (best effort) and `open` the bundle; the exit code of open is the launch
// signal. Linux: setsid'd child that must still be alive 1.5 s later.
// Untested on a real macOS or Linux machine.
func (a *unixApp) Launch(ctx context.Context, exe string, args []string) error {
	if a.goos == "darwin" {
		app := bundlePath(exe)
		if app == "" {
			return fmt.Errorf("%s is not inside a .app bundle; open it from Finder", exe)
		}
		_, _ = a.run(ctx, "/usr/bin/xattr", "-dr", "com.apple.quarantine", app)
		oargs := []string{app}
		if len(args) > 0 {
			oargs = append(oargs, "--args")
			oargs = append(oargs, args...)
		}
		if out, err := a.run(ctx, "/usr/bin/open", oargs...); err != nil {
			return fmt.Errorf("open %s: %w (%s)", app, err, strings.TrimSpace(out))
		}
		return nil
	}
	return a.launcher(ctx, exe, args)
}

// CanLaunch decides whether starting a GUI app makes sense: not over SSH,
// a display on Linux, and on Linux a usable Chromium sandbox (platforms.md
// section 3, copied from upstream's relaunch gate). Untested on a real macOS
// or Linux machine.
func (a *unixApp) CanLaunch(exe string) (bool, string) {
	if !a.exists(exe) {
		return false, "the desktop app is not at " + exe
	}
	if a.getenv("SSH_CONNECTION") != "" || a.getenv("SSH_TTY") != "" {
		return false, "this is an SSH session; reopen Hermes on the machine's own desktop"
	}
	if a.goos == "darwin" {
		return true, ""
	}
	if a.getenv("DISPLAY") == "" && a.getenv("WAYLAND_DISPLAY") == "" {
		return false, "no graphical session (DISPLAY/WAYLAND_DISPLAY is empty); reopen Hermes from your desktop"
	}
	if !a.sandboxUsable(exe) {
		return false, "the Chromium sandbox is not usable here (chrome-sandbox is not setuid root and user namespaces are off); reopen Hermes yourself"
	}
	return true, ""
}

// sandboxUsable mirrors upstream: an explicit opt-out, or a setuid-root
// chrome-sandbox beside the binary, or working user namespaces.
func (a *unixApp) sandboxUsable(exe string) bool {
	if a.getenv("ELECTRON_DISABLE_SANDBOX") != "" {
		return true
	}
	if a.chromeSandboxSetuid != nil && a.chromeSandboxSetuid(path.Join(path.Dir(exe), "chrome-sandbox")) {
		return true
	}
	return a.userNamespacesWork != nil && a.userNamespacesWork()
}
