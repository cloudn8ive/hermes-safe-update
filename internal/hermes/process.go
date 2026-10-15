package hermes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
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
	return ClassifyIn(p, home, checkout, desktopMarkers, nil)
}

// MacAppDirs picks the installed Hermes.app bundles out of the platform's
// packaged-install hints (/Applications, ~/Applications). `hermes update`
// copies the rebuilt app over them and refuses while one runs, so a running
// one is the desktop app of this install too.
func MacAppDirs(hints []platform.PackagedHint) []string {
	var dirs []string
	for _, h := range hints {
		if h.Kind == string(KindMacApp) && h.Path != "" {
			dirs = append(dirs, h.Path)
		}
	}
	return dirs
}

// ClassifyIn is Classify plus appDirs: installed app bundles
// (MacAppDirs) whose main executable under <bundle>/Contents/MacOS counts as
// the desktop app. Helpers in Contents/Frameworks never match, and --type=
// processes are dropped first as before.
func ClassifyIn(p platform.Process, home, checkout string, desktopMarkers, appDirs []string) string {
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
	if isHermesBinaryName(base) && isDesktopMain(exe, derived, flat, checkout, desktopMarkers, appDirs) {
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
	if strings.Contains(cmd, " serve") && (strings.Contains(cmd, "hermes.exe") || strings.Contains(cmd, "hermes_cli") || isHermesServeInvocation(cmd)) {
		return ClassBackend
	}
	return ""
}

// isHermesServeInvocation is the POSIX spelling of the backend command: the
// entry script run as `<path>/hermes serve ...` (directly or as the script
// argument of a venv python), or `hermes serve ...` as argv[0] looked up on
// PATH. Only reached for an interpreter or hermes executable that already
// lies under this install, so an editor or `rg` naming such a path is still
// not claimed. cmd is lower-case.
func isHermesServeInvocation(cmd string) bool {
	return hermesServeRe.MatchString(cmd)
}

var hermesServeRe = regexp.MustCompile(`(^|/)hermes serve( |$)`)

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

func isDesktopMain(exe string, derived bool, flatCmd, checkout string, markers, appDirs []string) bool {
	co := normPath(checkout)
	inRelease := co != "" && underDir(exe, co+"/apps/desktop/release")
	inApp := false
	for _, a := range appDirs {
		if a = normPath(a); a != "" && underDir(exe, a+"/contents/macos") {
			inApp = true
		}
	}
	if !inRelease && !inApp {
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

// Marker is the update-in-progress file shared with Hermes. It is written in
// Hermes' format v2 ("<pid>\n<epoch>\nct:<creation time>\n", see
// MarkerCreateTimeRe) whenever our own creation time can be read in Hermes'
// clock, so a reused pid cannot fake liveness and the claim does not age out
// after MarkerStaleAfterS; otherwise in format v1 ("<pid>\n<epoch>\n"). It
// reads both formats, including "delegate:" lines another updater appended.
type Marker struct {
	Home  string
	Pid   int // our pid
	Procs platform.Procs
	Now   func() time.Time
	// Created reads a process's creation time in the clock Hermes uses
	// (platform.ProcessCreated). nil or ok=false means unknown: the marker is
	// then written as v1 and a holder's creation time is not checked. A ct we
	// could not read with Hermes' precision is never written, because Hermes
	// would judge a mismatching claim dead.
	Created func(pid int) (time.Time, bool)

	// afterRead is a test seam: called by Refresh between its first read and
	// the re-read before the write (where another writer can slip in).
	afterRead func()
}

func (m *Marker) path() string { return joinPath(m.Home, MarkerFile) }

func (m *Marker) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// markerInfo is what a marker file says (see contracts.go for the format).
type markerInfo struct {
	pid         int      // line 1 (first token); 0 = unreadable
	ct          *float64 // line 3 "ct:<s>"; nil = format v1
	delegatePid int      // first well-formed "delegate:<pid> ct:<s>" line; 0 = none
	delegateCT  float64
}

// parseMarker reads marker bytes leniently: BOM, CRLF and spaces/tabs around
// a line are tolerated (as Hermes does), and the first token of line 1 is the
// pid. An unreadable pid leaves info.pid at 0.
func parseMarker(b []byte) markerInfo {
	text := strings.TrimPrefix(string(b), "\ufeff")
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = strings.Trim(strings.TrimSuffix(lines[i], "\r"), " \t")
	}
	var info markerInfo
	if f := strings.Fields(lines[0]); len(f) > 0 {
		if n, err := strconv.Atoi(f[0]); err == nil && n > 0 {
			info.pid = n
		}
	}
	if len(lines) > 2 {
		if c := MarkerCreateTimeRe.FindStringSubmatch(lines[2]); c != nil {
			if v, err := strconv.ParseFloat(c[1], 64); err == nil {
				info.ct = &v
			}
		}
	}
	if len(lines) > 3 {
		for _, l := range lines[3:] {
			d := MarkerDelegateRe.FindStringSubmatch(l)
			if d == nil {
				continue
			}
			pid, e1 := strconv.Atoi(d[1])
			v, e2 := strconv.ParseFloat(d[2], 64)
			if e1 == nil && e2 == nil && pid > 0 {
				info.delegatePid, info.delegateCT = pid, v
				break
			}
		}
	}
	return info
}

// readInfo returns the parsed marker; exists tells a file was there, ok that
// it names a pid.
func (m *Marker) readInfo() (info markerInfo, exists, ok bool) {
	b, err := os.ReadFile(m.path())
	if err != nil {
		return markerInfo{}, !errors.Is(err, fs.ErrNotExist), false
	}
	info = parseMarker(b)
	return info, true, info.pid > 0
}

// readPid returns the first token as a pid; ok=false when the file is
// missing, empty or unparseable. exists tells a file was there.
func (m *Marker) readPid() (pid int, exists, ok bool) {
	info, exists, ok := m.readInfo()
	return info.pid, exists, ok
}

// identityLive: pid is alive and, when the marker recorded a creation time
// and we can read the process's, it is the same incarnation (within Hermes'
// 2 s tolerance). A creation time we cannot read proves nothing against the
// pid, so the process counts as the holder (never guess it away).
func (m *Marker) identityLive(pid int, ct *float64) bool {
	if pid <= 0 || !m.Procs.Alive(pid) {
		return false
	}
	if ct == nil || m.Created == nil {
		return true
	}
	actual, ok := m.Created(pid)
	if !ok || actual.IsZero() {
		return true
	}
	return math.Abs(float64(actual.UnixNano())/1e9-*ct) <= MarkerCreateTimeToleranceS
}

// Holder returns the pid of a live process that holds the marker: the owner,
// else the delegate a v2 marker names. stale is true when a marker file exists
// but nobody live holds it (owner dead, pid reused by another process, or
// unreadable): the next claim replaces it.
func (m *Marker) Holder() (pid int, stale bool) {
	info, exists, ok := m.readInfo()
	if !exists {
		return 0, false
	}
	if ok && m.identityLive(info.pid, info.ct) {
		return info.pid, false
	}
	if ok && info.delegatePid > 0 && m.identityLive(info.delegatePid, &info.delegateCT) {
		return info.delegatePid, false
	}
	return 0, true
}

// ownCT is our creation time as the marker's "ct:" text (unix seconds, three
// decimals, like Hermes' f"{ct:.3f}"), or ok=false when it cannot be read.
func (m *Marker) ownCT() (string, bool) {
	if m.Created == nil {
		return "", false
	}
	t, ok := m.Created(m.Pid)
	if !ok || t.IsZero() {
		return "", false
	}
	ms := (t.UnixNano() + 500_000) / 1_000_000
	return fmt.Sprintf("%d.%03d", ms/1000, ms%1000), true
}

func (m *Marker) content() []byte {
	body := fmt.Sprintf("%d\n%d\n", m.Pid, m.now().Unix())
	if ct, ok := m.ownCT(); ok {
		body += "ct:" + ct + "\n"
	}
	return []byte(body)
}

// Claim writes the marker atomically. Call it only after the point of no
// return (the close step), so an abort leaves no marker.
func (m *Marker) Claim() error { return fsx.WriteFileAtomic(m.path(), m.content(), 0o644) }

// Refresh renews the started-at line of the marker if it is still ours and
// leaves every other line byte for byte as it is: the "ct:" line (our
// creation time never changes) and any "delegate:" or "run:" line that
// Hermes or another updater wrote in.
//
// Hermes appends its "delegate:" line under its own marker mutex, which we do
// not hold, so the write is compare-and-write: the file is read again right
// before the atomic replace, and if it changed since the first read (a line
// was appended, the marker was replaced) the refresh starts over from the new
// content. A line written by someone else is never dropped. What is left is
// the instant between that second read and the rename; if the file keeps
// changing the refresh is skipped for this tick (the next one, two minutes
// later, retries; the stale limit is twenty).
func (m *Marker) Refresh() error {
	for attempt := 0; attempt < 3; attempt++ {
		b, err := os.ReadFile(m.path())
		if err != nil {
			return nil
		}
		if info := parseMarker(b); info.pid != m.Pid {
			return nil
		}
		next := m.withStartedAt(b)
		if m.afterRead != nil {
			m.afterRead()
		}
		cur, err := os.ReadFile(m.path())
		if err != nil {
			return nil
		}
		if !bytes.Equal(cur, b) {
			continue // changed under us: redo from what is there now
		}
		return fsx.WriteFileAtomic(m.path(), next, 0o644)
	}
	return nil
}

// withStartedAt returns marker bytes b with line 2 replaced by the current
// epoch. A marker without a line 2 is written afresh.
func (m *Marker) withStartedAt(b []byte) []byte {
	epoch := strconv.FormatInt(m.now().Unix(), 10)
	nl1 := bytes.IndexByte(b, '\n')
	if nl1 < 0 {
		return m.content()
	}
	head, rest := b[:nl1+1], b[nl1+1:]
	if nl2 := bytes.IndexByte(rest, '\n'); nl2 >= 0 {
		eol := "\n"
		if nl2 > 0 && rest[nl2-1] == '\r' {
			eol = "\r\n"
		}
		return append(append(append([]byte{}, head...), epoch+eol...), rest[nl2+1:]...)
	}
	return append(append([]byte{}, head...), epoch+"\n"...)
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
