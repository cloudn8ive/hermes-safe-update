package hermes

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/fsx"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// Process classes returned by Classify.
const (
	ClassDesktop = "desktop"
	ClassKernel  = "kernel"
	ClassGateway = "gateway"
	ClassBackend = "backend"
)

// classOrder is the stable display order of CountByClass.
var classOrder = []string{ClassDesktop, ClassGateway, ClassBackend, ClassKernel}

// Classify says what part of Hermes a process is, or "" (python-behaviour
// §2.0, first match wins). Its answer drives close and kill decisions, so it
// only claims processes that provably belong to THIS install (safety review
// SR-1): home and checkout are the install's paths, desktopMarkers are the
// lower-case command-line substrings of the desktop main process
// (platform.Paths.DesktopProcessMarkers).
//
// The executable must lie under the install: p.Exe when known, otherwise the
// first command-line token, and only if that token is an absolute path (see
// DECISIONS.md, SR-1F). A bare `python.exe <home>\x gateway run` is not claimed.
//
//   - desktop: the executable lies under <checkout>/apps/desktop/release/ (a
//     cmdline-derived executable must also carry a desktop marker).
//   - kernel, gateway, backend: the executable lies under home or checkout, is
//     a Python interpreter or hermes(.exe), and the command line has the
//     kernel / "gateway run" / "serve" marker.
//
// Electron helpers (--type=) never count.
func Classify(p platform.Process, home, checkout string, desktopMarkers []string) string {
	cmd := strings.ToLower(p.Cmdline)
	if cmd == "" {
		return ""
	}
	// Electron helpers (renderer, GPU, utility) carry --type=; only the main
	// process counts as "the desktop app".
	if strings.Contains(cmd, "--type=") {
		return ""
	}
	flat := strings.ReplaceAll(cmd, "\\", "/")
	exe, derived := effectiveExe(p)
	if exe == "" {
		return ""
	}
	base := exeBase(p, exe)
	if isHermesBinaryName(base) && isDesktopMain(exe, derived, flat, checkout, desktopMarkers) {
		return ClassDesktop
	}
	if !isInterpreterName(base) {
		return ""
	}
	if !underDir(exe, normPath(home)) && !underDir(exe, normPath(checkout)) {
		return ""
	}
	if strings.Contains(cmd, "hermes_kernel_runner.py") {
		return ClassKernel
	}
	if strings.Contains(cmd, " gateway run") {
		return ClassGateway
	}
	if strings.Contains(cmd, " serve") && (strings.Contains(cmd, "hermes.exe") || strings.Contains(cmd, "hermes_cli")) {
		return ClassBackend
	}
	return ""
}

// effectiveExe is the normPath'd executable path: p.Exe, else the first
// command-line token when it is an absolute path. derived reports the latter.
// "" means the executable is unknown.
func effectiveExe(p platform.Process) (exe string, derived bool) {
	if e := normPath(p.Exe); e != "" {
		return e, false
	}
	c := strings.TrimSpace(p.Cmdline)
	tok := ""
	if strings.HasPrefix(c, `"`) {
		if k := strings.Index(c[1:], `"`); k >= 0 {
			tok = c[1 : 1+k]
		}
	} else {
		tok, _, _ = strings.Cut(c, " ")
	}
	if !isAbsPath(tok) {
		return "", true
	}
	return normPath(tok), true
}

// isAbsPath: drive-letter, UNC or rooted slash path (any OS's form).
func isAbsPath(s string) bool {
	if len(s) >= 3 && s[1] == ':' && (s[2] == '\\' || s[2] == '/') &&
		(s[0]|0x20 >= 'a' && s[0]|0x20 <= 'z') {
		return true
	}
	return strings.HasPrefix(s, "/") || strings.HasPrefix(s, `\\`)
}

func isDesktopMain(exe string, derived bool, flatCmd, checkout string, markers []string) bool {
	co := normPath(checkout)
	if co == "" || !underDir(exe, co+"/apps/desktop/release") {
		return false
	}
	return !derived || hasAny(flatCmd, markers)
}

// normPath lower-cases, uses forward slashes and cleans; "" stays "".
func normPath(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return path.Clean(strings.ToLower(strings.ReplaceAll(s, "\\", "/")))
}

// underDir reports whether p equals dir or lies below it (both normPath'd).
func underDir(p, dir string) bool {
	return dir != "" && (p == dir || strings.HasPrefix(p, strings.TrimRight(dir, "/")+"/"))
}

// exeBase is the lower-case executable file name: Name when the OS gave one
// (so a renamed binary is judged by what it is called), else the file name of
// the effective executable path exe.
func exeBase(p platform.Process, exe string) string {
	src := p.Name
	if p.Exe != "" || src == "" {
		src = exe
	}
	src = strings.ReplaceAll(src, "\\", "/")
	return strings.ToLower(src[strings.LastIndex(src, "/")+1:])
}

var interpreterName = regexp.MustCompile(`^pythonw?[0-9.]*(\.exe)?$|^hermes(\.exe)?$`)

func isInterpreterName(base string) bool { return interpreterName.MatchString(base) }

func isHermesBinaryName(n string) bool {
	return n == "hermes.exe" || n == "hermes" || strings.HasPrefix(n, "hermes ")
}

func hasAny(s string, subs []string) bool {
	for _, m := range subs {
		if m != "" && strings.Contains(s, strings.ToLower(strings.ReplaceAll(m, "\\", "/"))) {
			return true
		}
	}
	return false
}

// CountByClass counts classified processes; "" entries are ignored.
func CountByClass(classes []string) map[string]int {
	out := map[string]int{}
	for _, c := range classes {
		if c != "" {
			out[c]++
		}
	}
	return out
}

// FormatCounts renders counts in stable order ("desktop 1, gateway 2") or "none".
func FormatCounts(counts map[string]int) string {
	var parts []string
	for _, c := range classOrder {
		if n := counts[c]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", c, n))
		}
	}
	var extra []string
	for c, n := range counts {
		known := false
		for _, k := range classOrder {
			known = known || k == c
		}
		if !known && n > 0 {
			extra = append(extra, fmt.Sprintf("%s %d", c, n))
		}
	}
	sort.Strings(extra)
	parts = append(parts, extra...)
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// Marker is the update-in-progress file shared with Hermes (format
// "<pid>\n<epoch>\n"; Hermes treats it as stale after MarkerStaleAfterS).
type Marker struct {
	Home  string
	Pid   int // our pid
	Procs platform.Procs
	Now   func() time.Time
}

func (m *Marker) path() string { return joinPath(m.Home, MarkerFile) }

func (m *Marker) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// readPid returns the first token as a pid; ok=false when the file is
// missing, empty or unparseable. exists tells a file was there.
func (m *Marker) readPid() (pid int, exists, ok bool) {
	b, err := os.ReadFile(m.path())
	if err != nil {
		return 0, !errors.Is(err, fs.ErrNotExist), false
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0, true, false
	}
	n, err := strconv.Atoi(f[0])
	if err != nil || n <= 0 {
		return 0, true, false
	}
	return n, true, true
}

// Holder returns the pid of a live process that holds the marker. stale is
// true when a marker file exists but its owner is dead or unreadable (the
// next claim replaces it).
func (m *Marker) Holder() (pid int, stale bool) {
	p, exists, ok := m.readPid()
	if !exists {
		return 0, false
	}
	if ok && m.Procs.Alive(p) {
		return p, false
	}
	return 0, true
}

func (m *Marker) content() []byte {
	return []byte(fmt.Sprintf("%d\n%d\n", m.Pid, m.now().Unix()))
}

// Claim writes the marker atomically. Call it only after the point of no
// return (the close step), so an abort leaves no marker.
func (m *Marker) Claim() error { return fsx.WriteFileAtomic(m.path(), m.content(), 0o644) }

// Refresh rewrites the marker if it is still ours; otherwise it does nothing.
func (m *Marker) Refresh() error {
	if p, _, ok := m.readPid(); !ok || p != m.Pid {
		return nil
	}
	return fsx.WriteFileAtomic(m.path(), m.content(), 0o644)
}

// Release deletes the marker only if it is ours. released reports whether
// a file was removed.
func (m *Marker) Release() (released bool, err error) {
	if p, _, ok := m.readPid(); !ok || p != m.Pid {
		return false, nil
	}
	if err := os.Remove(m.path()); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("release update marker %q: %w", m.path(), err)
	}
	return true, nil
}

// RunRefresher calls Refresh on every tick until ctx ends (blocking; run it
// in a goroutine). Pass time.NewTicker(cfg.MarkerRefresh()).C.
func (m *Marker) RunRefresher(ctx context.Context, ticks <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			_ = m.Refresh() // best effort; the stale limit is 20 min, we refresh every 2
		}
	}
}
