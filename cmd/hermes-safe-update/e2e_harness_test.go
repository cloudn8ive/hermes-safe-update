package main

// End-to-end replay harness: runs the real CLI (run(), the real wiring in
// wire.go, the real update engine, the real hermes/settings/mirror/gc/cua
// services and the real renderers) against a synthetic Hermes install in a
// temp dir. Every system effect is a fake: processes, app close/launch,
// every child process (git, `hermes update`, the gateway probe, Electron,
// the GC script, cua, hooks), the GitHub API (httptest), the clock and the
// console. Any child process the script does not expect fails the test,
// like the Python replay.py ("any system call not covered here raises").
//
// Synthetic data only: user path C:\Users\you (on screen), session title
// "Planning the week", made-up SHAs and pids.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
	"github.com/cloudn8ive/hermes-safe-update/internal/testutil"
	"github.com/cloudn8ive/hermes-safe-update/internal/ui"
)

const (
	e2eOld = "5d41402abc4b2a76b9719d911017c592a1b2c3d4"
	e2eNew = "7f9c2ba4e88f827d616045507605853e00e1f2a3"

	pidDesktop  = 4242
	pidGateway  = 8211
	pidGateway2 = 8324
	pidBackend  = 5310
	pidUpdate   = 9100
	pidUpdateCh = 9101

	targetOrigin = "http://127.0.0.1:47891"
	shownHome    = `C:\Users\you\AppData\Local\hermes`
)

// e2eStart is the replay's virtual start time (the Python replay's run).
var e2eStart = time.Date(2026, 9, 28, 9, 47, 21, 0, time.UTC)

// updateLine is one line of scripted `hermes update` output: the clock
// advances by adv before the line appears; do runs before it is emitted.
type updateLine struct {
	adv  time.Duration
	line string
	do   func(w *e2eWorld)
}

type e2eWorld struct {
	t     *testing.T
	clock *testutil.Clock

	root, home, checkout, launcher, git, python string
	electron, desktopExe, userData, cuaExe      string

	selfPID int // the updater's pid in the fake table (0 = real)

	plat  *platform.Platform
	procs *platform.FakeProcs
	app   *e2eApp
	con   *scriptConsole
	api   *httptest.Server

	// scripted behaviour (set before cli())
	vt          bool
	yaml        string
	apiStatus   string // "ahead" (default), "identical", "down"
	checkOut    string // `hermes update --check` output when the API is down
	update      []updateLine
	updateRC    int
	updateHang  bool // the update never ends: the 90-min timeout fires
	verifyOK    bool
	staleGW     bool     // a restarted gateway still reports the old code
	probes      []string // successive probe verdicts: "ok" | "blocked"
	migrateFail string   // "" | "write"
	failDumpAt  int      // n-th migrator dump fails (0 = never)
	hookTimeout bool
	onSleep     func(w *e2eWorld)
	pace        float64 // >0: replay in real time at this speed (HSU_REPLAY_LIVE)
	live        io.Writer

	mu        sync.Mutex
	head      string
	gwSHA     string
	calls     [][]string
	mutations []string
	hookEnv   []map[string]string
	dumps     int
	probeN    int

	out, errb bytes.Buffer
	code      int
}

func newWorld(t *testing.T) *e2eWorld {
	t.Helper()
	w := &e2eWorld{t: t, clock: testutil.NewClock(e2eStart), head: e2eOld, gwSHA: e2eOld,
		verifyOK: true, apiStatus: "ahead", yaml: "mirror:\n  offer: false\n"}
	w.root = t.TempDir()
	w.home = filepath.Join(w.root, "AppData", "Local", "hermes")
	w.checkout = filepath.Join(w.home, "hermes-agent")
	w.launcher = filepath.Join(w.home, "bin", "hermes.exe")
	w.git = filepath.Join(w.home, "tools", "git-2.53", "cmd", "git.exe")
	w.python = filepath.Join(w.home, "python", "3.12", "python.exe")
	w.electron = filepath.Join(w.checkout, "apps", "desktop", "node_modules", "electron", "dist", "electron.exe")
	w.desktopExe = filepath.Join(w.checkout, "apps", "desktop", "release", "win-unpacked", "Hermes.exe")
	w.userData = filepath.Join(w.root, "AppData", "Roaming", "Hermes")
	w.cuaExe = filepath.Join(w.home, "tools", "cua-driver-0.21.0-win32-x64", "cua-driver.exe")

	mkdir(t, filepath.Join(w.checkout, ".git"))
	for _, f := range []string{w.launcher, w.git, w.python, w.electron, w.desktopExe} {
		writeFile(t, f, "x")
	}
	writeFile(t, filepath.Join(w.checkout, "apps", "desktop", "node_modules", "electron", "path.txt"), "electron.exe")
	writeFile(t, filepath.Join(w.checkout, filepath.FromSlash(hermes.RendererServerSource)), "export const DEFAULT_PORT = 47891\n")
	w.writeGatewayState(e2eOld, pidGateway)
	w.writeStore(map[string]map[string]string{
		"file://": {
			"hermes.desktop.theme":                         `"dark"`,
			"hermes.desktop.sidebarWidth":                  `280`,
			"hermes.desktop.threadScroll.v1.profile.work":  `{"s1":120}`,
			"hermes.desktop.lastRoute.profile.work":        `"/chat/s1"`,
			"hermes.desktop.sessionSeenCounts":             `{"s1":4}`,
			"hermes.desktop.tips.next.v1":                  `3`,
			"hermes.desktop.composer.sendOnEnter":          `true`,
			"hermes.desktop.unreadFinishedSessions":        `["s1"]`,
			"hermes.desktop.toolDisclosure.v1":             `{"bash":true}`,
			"hermes.desktop.lastSessionId.profile.default": `"s1"`,
		},
		targetOrigin: {
			"hermes.desktop.tips.next.v1": `1`,
			"hermes.desktop.theme":        `"light"`,
		},
	})
	w.createSessionsDB()

	w.plat = platform.NewFake()
	w.plat.OS = "windows"
	w.procs = w.plat.Procs.(*platform.FakeProcs)
	w.app = &e2eApp{w: w}
	w.plat.App = w.app
	w.con = &scriptConsole{w: w}
	w.plat.Console = w.con
	w.plat.Disk = &platform.FakeDisk{FreeBytes: 41_700_000_000}
	w.plat.Autostart = &platform.FakeAutostart{Tasks: map[string]string{hermes.CuaTaskName: w.cuaExe}}
	fp := w.plat.Paths.(*platform.FakePaths)
	fp.Home = w.home
	fp.UserDataDir = w.userData
	fp.Launchers = []string{w.launcher}
	fp.DesktopApps = []string{w.desktopExe}
	fp.Markers = []string{"win-unpacked"}
	fp.PythonGlobs = []string{filepath.Join(w.home, "python", "*", "python.exe")}
	fp.GitGlobs = []string{filepath.Join(w.home, "tools", "git-*", "cmd", "git.exe")}
	fp.Electron = "electron.exe"

	w.procs.Add(platform.Process{PID: pidDesktop, Name: "Hermes.exe", Cmdline: `"` + w.desktopExe + `"`})
	w.procs.Add(w.gatewayProc(pidGateway))
	w.procs.Add(platform.Process{PID: pidBackend, Name: "hermes.exe", Cmdline: w.launcher + " serve --port 0"})

	w.api = httptest.NewServer(http.HandlerFunc(w.serveAPI))
	t.Cleanup(w.api.Close)
	w.update = happyUpdate()
	return w
}

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, p, s string) {
	t.Helper()
	mkdir(t, filepath.Dir(p))
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (w *e2eWorld) gatewayProc(pid int) platform.Process {
	return platform.Process{PID: pid, Name: "python.exe",
		Cmdline: filepath.Join(w.home, "venv", "Scripts", "python.exe") + " -m hermes_cli.main gateway run"}
}

func (w *e2eWorld) writeGatewayState(sha string, pid int) {
	b, _ := json.Marshal(map[string]any{"code_sha": sha, "pid": pid})
	writeFile(w.t, filepath.Join(w.home, hermes.GatewayStateFile), string(b))
}

// ---- clock ----

// advance moves the virtual clock (and, when replaying live, really waits).
func (w *e2eWorld) advance(d time.Duration) {
	if d <= 0 {
		return
	}
	if w.pace > 0 {
		time.Sleep(time.Duration(float64(d) / w.pace))
	}
	w.clock.Advance(d)
}

func (w *e2eWorld) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	w.advance(d)
	if w.onSleep != nil {
		w.onSleep(w)
	}
	return ctx.Err()
}

// ---- sessions (a real SQLite state.db, read by the real detector) ----

func (w *e2eWorld) db() *sql.DB {
	db, err := sql.Open("sqlite", filepath.Join(w.home, "state.db"))
	if err != nil {
		w.t.Fatal(err)
	}
	return db
}

func (w *e2eWorld) exec(q string, args ...any) {
	db := w.db()
	defer db.Close()
	if _, err := db.Exec(q, args...); err != nil {
		w.t.Fatal(err)
	}
}

func (w *e2eWorld) createSessionsDB() {
	w.exec(`create table sessions (id text primary key, title text, source text,
		started_at real, last_activity_at real, ended_at real)`)
	w.exec(`insert into sessions values ('20260101_000002_cccccc', 'Old finished chat', 'desktop', ?, ?, ?)`,
		float64(e2eStart.Add(-20*time.Hour).Unix()), float64(e2eStart.Add(-19*time.Hour).Unix()), float64(e2eStart.Add(-19*time.Hour).Unix()))
}

// busySession starts "Planning the week", last active a few seconds ago.
func (w *e2eWorld) busySession() {
	now := w.clock.Now()
	w.exec(`insert into sessions values ('20260101_000000_aaaaaa', 'Planning the week', 'desktop', ?, ?, null)`,
		float64(now.Add(-3*time.Minute).Unix()), float64(now.Add(-42*time.Second).Unix()))
}

func (w *e2eWorld) touchSession() {
	w.exec(`update sessions set last_activity_at = ? where id = '20260101_000000_aaaaaa'`, float64(w.clock.Now().Unix()))
}

func (w *e2eWorld) endSession() {
	w.exec(`update sessions set ended_at = ? where id = '20260101_000000_aaaaaa'`, float64(w.clock.Now().Unix()))
}

// ---- GitHub API (httptest; the real mirror service talks to it) ----

func (w *e2eWorld) serveAPI(rw http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/repos/NousResearch/hermes-agent/compare/"+e2eOld+"...main") || w.apiStatus == "down" {
		rw.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if w.apiStatus == "identical" {
		_, _ = io.WriteString(rw, `{"status":"identical","ahead_by":0,"files":[]}`)
		return
	}
	_, _ = io.WriteString(rw, `{"status":"ahead","ahead_by":3,"files":[`+
		`{"filename":"hermes_cli/update_cmd.py"},{"filename":"apps/desktop/package.json"},{"filename":"README.md"}]}`)
}

// ---- console ----

// conStep is one scripted answer: a key, or a prompt timeout.
type conStep struct {
	key     rune
	timeout bool
	then    func(w *e2eWorld)
}

type scriptConsole struct {
	w        *e2eWorld
	mu       sync.Mutex
	steps    []conStep
	terminal bool
	prompts  int
}

func (c *scriptConsole) script(s ...conStep) {
	c.mu.Lock()
	c.steps = append(c.steps, s...)
	c.mu.Unlock()
}

func (c *scriptConsole) IsTerminal() bool                    { return c.terminal }
func (c *scriptConsole) EnableVT() (func(), error)           { return func() {}, nil }
func (c *scriptConsole) Size() (int, int, bool)              { return 100, 30, true }
func (c *scriptConsole) SetTitle(string) error               { return nil }
func (c *scriptConsole) SetIdentity(string, []string) string { return "fake" }
func (c *scriptConsole) IsForeground() bool                  { return true }
func (c *scriptConsole) Flash()                              {}

func (c *scriptConsole) ReadKey(ctx context.Context) (platform.Key, error) {
	c.mu.Lock()
	c.prompts++
	if len(c.steps) == 0 {
		c.mu.Unlock()
		c.w.t.Errorf("replay: unexpected prompt (no scripted key left)")
		return platform.Key{}, context.DeadlineExceeded
	}
	s := c.steps[0]
	c.steps = c.steps[1:]
	c.mu.Unlock()
	if s.timeout {
		if dl, ok := ctx.Deadline(); ok {
			c.w.advance(time.Until(dl).Round(time.Second))
		}
		if s.then != nil {
			s.then(c.w)
		}
		return platform.Key{}, context.DeadlineExceeded
	}
	c.w.advance(2 * time.Second) // the user's reaction time
	if s.then != nil {
		s.then(c.w)
	}
	return platform.Key{Rune: s.key}, nil
}

func (c *scriptConsole) left() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.steps) }

// ---- app control ----

type e2eApp struct {
	w           *e2eWorld
	mu          sync.Mutex
	calls       []string
	ignoreClose bool
	forceFails  bool
	blocker     string
	closedAt    time.Time
	onForce     func()
}

func (a *e2eApp) record(s string) { a.mu.Lock(); a.calls = append(a.calls, s); a.mu.Unlock() }
func (a *e2eApp) Calls() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.calls...)
}

func (a *e2eApp) IsRunning(pid int) bool { return a.w.procs.Alive(pid) }
func (a *e2eApp) RequestClose(_ context.Context, pid int) error {
	a.record(fmt.Sprintf("close %d", pid))
	a.closedAt = a.w.clock.Now()
	if !a.ignoreClose {
		a.w.procs.Remove(pid)
	}
	return nil
}
func (a *e2eApp) ForceClose(_ context.Context, p platform.Process) error {
	pid := p.PID
	a.record(fmt.Sprintf("force %d", pid))
	if a.onForce != nil {
		a.onForce()
	}
	if a.forceFails {
		return errors.New("access is denied")
	}
	a.w.procs.Remove(pid)
	return nil
}
func (a *e2eApp) CloseBlocker(int) string { return a.blocker }
func (a *e2eApp) Launch(_ context.Context, exe string, _ []string) error {
	a.record("launch " + filepath.Base(exe))
	return nil
}
func (a *e2eApp) CanLaunch(string) (bool, string) { return true, "" }

// ---- child processes ----

func (w *e2eWorld) mutate(what string) {
	w.mu.Lock()
	w.mutations = append(w.mutations, what)
	w.mu.Unlock()
}

func (w *e2eWorld) Mutations() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.mutations...)
}

func (w *e2eWorld) Calls() [][]string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([][]string(nil), w.calls...)
}

func (w *e2eWorld) ran(prefix ...string) bool {
	for _, c := range w.Calls() {
		if len(c) >= len(prefix) && strings.Join(c[:len(prefix)], "\x00") == strings.Join(prefix, "\x00") {
			return true
		}
	}
	return false
}

func (w *e2eWorld) unexpected(c execx.Cmd) (execx.Result, error) {
	w.t.Errorf("replay: unexpected command %q", c.Argv)
	return execx.Result{Code: execx.CodeNotFound, Output: "replay: unexpected command"}, nil
}

func (w *e2eWorld) Run(ctx context.Context, c execx.Cmd) (execx.Result, error) {
	if len(c.Argv) == 0 {
		return execx.Result{}, execx.ErrEmptyArgv
	}
	w.mu.Lock()
	w.calls = append(w.calls, append([]string(nil), c.Argv...))
	w.mu.Unlock()
	a0, rest := c.Argv[0], c.Argv[1:]
	switch {
	case a0 == w.launcher:
		return w.hermesCLI(c, rest)
	case a0 == w.git:
		return w.gitCmd(c, rest)
	case a0 == w.python:
		return w.pythonCmd(c, rest)
	case a0 == w.electron:
		return w.migrator(c, rest)
	case a0 == "hermes-tiles.exe":
		return w.hook(c)
	}
	return w.unexpected(c)
}

func (w *e2eWorld) hermesCLI(c execx.Cmd, rest []string) (execx.Result, error) {
	switch strings.Join(rest, " ") {
	case "--version":
		v := "v0.21.5" // the launcher reports the new version once the pull moved HEAD
		if w.Head() == e2eNew {
			v = "v0.21.6"
		}
		return execx.Result{Output: "Hermes Agent " + v + " (2026.9.24)\nInstall directory: " + w.checkout + "\nInstall method: git\n"}, nil
	case "update --check":
		w.advance(75 * time.Second)
		return execx.Result{Output: w.checkOut}, nil
	case "gateway stop", "gateway stop --all":
		w.mutate("gateway stop")
		w.removeGateways()
		return execx.Result{Output: "Gateway stopped."}, nil
	case "gateway start --all":
		w.mutate("gateway start")
		w.startGateway()
		return execx.Result{Output: "Gateway started."}, nil
	case strings.Join(hermes.VerifyModuleArgv, " "):
		w.advance(3 * time.Second)
		if w.verifyOK {
			return execx.Result{}, nil
		}
		return execx.Result{Code: 1, Output: "desktop build is older than the checkout"}, nil
	case "computer-use status":
		return execx.Result{Output: "cua-driver: installed at " + w.cuaExe + " (0.21.0)\n"}, nil
	case "computer-use install --upgrade":
		w.mutate("cua install")
		w.advance(4 * time.Second)
		return execx.Result{Output: "Preparing the pinned cua-driver with Hermes PM...\ncua-driver ready: 0.21.0.\n"}, nil
	}
	return w.unexpected(c)
}

func (w *e2eWorld) removeGateways() {
	ps, _ := w.procs.List(context.Background())
	for _, p := range ps {
		if strings.Contains(p.Cmdline, " gateway run") {
			w.procs.Remove(p.PID)
		}
	}
}

func (w *e2eWorld) startGateway() {
	w.mu.Lock()
	sha := w.head
	if w.staleGW {
		sha = e2eOld
	}
	w.gwSHA = sha
	w.mu.Unlock()
	w.removeGateways()
	w.procs.Add(w.gatewayProc(pidGateway2))
	w.writeGatewayState(sha, pidGateway2)
}

func (w *e2eWorld) gitCmd(c execx.Cmd, rest []string) (execx.Result, error) {
	want := []string{"-c", "gc.auto=0", "-C", w.checkout}
	if len(rest) < len(want) || strings.Join(rest[:4], " ") != strings.Join(want, " ") {
		return w.unexpected(c)
	}
	w.mu.Lock()
	head := w.head
	w.mu.Unlock()
	switch args := strings.Join(rest[4:], " "); {
	case args == "rev-parse --short HEAD":
		return execx.Result{Output: head[:7] + "\n"}, nil
	case args == "rev-parse HEAD":
		return execx.Result{Output: head + "\n"}, nil
	case strings.HasPrefix(args, "status --porcelain"):
		return execx.Result{}, nil
	case strings.HasPrefix(args, "diff --quiet HEAD origin/main"):
		if strings.Contains(args, "package") {
			return execx.Result{Code: 1}, nil // node packages changed
		}
		return execx.Result{}, nil
	}
	return w.unexpected(c)
}

func (w *e2eWorld) pythonCmd(c execx.Cmd, rest []string) (execx.Result, error) {
	if len(rest) >= 2 && rest[0] == "-I" && rest[1] == "-c" {
		// pause-gate probe (runs beside the update check: never advances the clock)
		w.mu.Lock()
		v := "ok"
		if len(w.probes) > 0 {
			v = w.probes[min(w.probeN, len(w.probes)-1)]
		}
		w.probeN++
		w.mu.Unlock()
		if v == "blocked" {
			return execx.Result{Output: hermes.GateProbeTag + `{"ok": false, "error": "RuntimeError", "chain": ["Could not map Windows gateway PIDs to profiles", "RuntimeError: Could not map Windows gateway PIDs to profiles: access denied for pid ` + fmt.Sprint(pidGateway) + `"]}` + "\n"}, nil
		}
		return execx.Result{Output: hermes.GateProbeTag + fmt.Sprintf(`{"ok": true, "gateways": [["default", %d]]}`, pidGateway) + "\n"}, nil
	}
	if len(rest) >= 2 && rest[0] == "-I" && strings.HasSuffix(rest[1], "safe-update-gc.py") {
		w.advance(2 * time.Second)
		if strings.Contains(strings.Join(rest, " "), "--dry-run") {
			return execx.Result{Output: "REMOVE (~310 MB) environments/1f2e3d4c\nREMOVE (~302 MB) environments/9a8b7c6d\n"}, nil
		}
		w.mutate("gc")
		return execx.Result{Output: "generation cleanup: 2 dependency generation(s), 0 PM runtime generation(s) removed; disk free change +612 MB\n"}, nil
	}
	return w.unexpected(c)
}

func (w *e2eWorld) hook(c execx.Cmd) (execx.Result, error) {
	w.mu.Lock()
	w.hookEnv = append(w.hookEnv, c.Env)
	w.mu.Unlock()
	w.mutate("hook " + c.Env["HERMES_SAFE_UPDATE_STAGE"])
	if w.hookTimeout {
		w.advance(c.Timeout)
		return execx.Result{Code: execx.CodeTimeout, TimedOut: true, Output: "refreshing tiles...", Duration: c.Timeout}, nil
	}
	w.advance(2 * time.Second)
	return execx.Result{Output: "refreshing tiles...\nStart tile restored", Duration: 2 * time.Second}, nil
}

// Stream is only used for `hermes update`.
func (w *e2eWorld) Stream(ctx context.Context, c execx.Cmd, onLine func(string)) (execx.Result, error) {
	want := append([]string{w.launcher}, hermes.UpdateArgv...)
	if strings.Join(c.Argv, "\x00") != strings.Join(want, "\x00") {
		w.t.Errorf("replay: unexpected streamed command %q", c.Argv)
		return execx.Result{Code: execx.CodeNotFound}, nil
	}
	w.mu.Lock()
	w.calls = append(w.calls, append([]string(nil), c.Argv...))
	w.mu.Unlock()
	w.mutate("update")
	w.procs.Add(platform.Process{PID: pidUpdate, Name: "hermes.exe", Cmdline: strings.Join(c.Argv, " ")})
	w.procs.Add(platform.Process{PID: pidUpdateCh, PPID: pidUpdate, Name: "node.exe", Cmdline: "node.exe npm-cli.js ci"})
	var lines []string
	for _, l := range w.update {
		w.advance(l.adv)
		if l.do != nil {
			l.do(w)
		}
		lines = append(lines, l.line)
		onLine(l.line)
	}
	out := strings.Join(lines, "\n")
	if w.updateHang {
		w.advance(c.Timeout)
		if c.KillTree == nil {
			w.t.Error("the update has no tree-kill hook for its timeout")
		} else if err := c.KillTree(pidUpdate); err != nil {
			w.t.Errorf("KillTree: %v", err)
		}
		return execx.Result{Code: execx.CodeTimeout, TimedOut: true, Output: out, Duration: c.Timeout}, ctx.Err()
	}
	w.procs.Remove(pidUpdate)
	return execx.Result{Code: w.updateRC, Output: out}, ctx.Err()
}

// happyUpdate is a synthetic `hermes update` run whose lines hit every
// step trigger (contracts.go), including the npm/frontend one.
func happyUpdate() []updateLine {
	return []updateLine{
		{0, "→ Update channel: main", nil},
		{2 * time.Second, fmt.Sprintf("→ Stopping Windows gateway (pid %d) so files can be replaced…", pidGateway), (*e2eWorld).removeGateways},
		{21 * time.Second, "→ Fetching updates...", nil},
		{9 * time.Second, "→ Pulling updates...", func(w *e2eWorld) { w.setHead(e2eNew) }},
		{88 * time.Second, "  Fast-forward " + e2eOld[:7] + ".." + e2eNew[:7] + " (3 files changed)", nil},
		{time.Second, "→ Updating Python dependencies (uv sync)…", nil},
		{62 * time.Second, "  ✓ Python dependencies are current", nil},
		{time.Second, "→ Preparing Node dependencies… (package.json changed: npm ci)", nil},
		{24 * time.Second, "→ Building the TUI…", nil},
		{30 * time.Second, "→ Building desktop packaged app…", nil},
		{205 * time.Second, "→ Packaging the desktop app…", nil},
		{21 * time.Second, "✓ Code updated!", nil},
		{time.Second, "→ Windows cua-driver refresh deferred (autostart registration requires UAC).", nil},
		{0, "  Run `hermes computer-use install --upgrade` in an interactive terminal.", nil},
		{10 * time.Second, "→ Restarting the gateway…", (*e2eWorld).startGateway},
		{20 * time.Second, "✓ Update complete! (v0.21.5 → v0.21.6)", nil},
	}
}

func (w *e2eWorld) setHead(h string) { w.mu.Lock(); w.head = h; w.mu.Unlock() }
func (w *e2eWorld) Head() string     { w.mu.Lock(); defer w.mu.Unlock(); return w.head }

// ---- fake Electron migrator (the settings service's storeIO runs it) ----

func (w *e2eWorld) storePath(userData string) string {
	return filepath.Join(userData, "Local Storage", "leveldb", "000003.log")
}

// writeStore writes a LevelDB-looking file: origins in key position (what
// the real origin scan looks for), then the values as JSON for the fake.
func (w *e2eWorld) writeStoreAt(userData string, s map[string]map[string]string) {
	var b strings.Builder
	var origins []string
	for o := range s {
		origins = append(origins, o)
	}
	sort.Strings(origins)
	for _, o := range origins {
		b.WriteString("META:" + o + "\n")
	}
	j, _ := json.Marshal(s)
	b.WriteString("--\n")
	b.Write(j)
	writeFile(w.t, w.storePath(userData), b.String())
	writeFile(w.t, filepath.Join(userData, "Local Storage", "leveldb", "CURRENT"), "MANIFEST-000001\n")
}

func (w *e2eWorld) writeStore(s map[string]map[string]string) { w.writeStoreAt(w.userData, s) }

func readStoreAt(p string) (map[string]map[string]string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	_, j, ok := strings.Cut(string(b), "--\n")
	if !ok {
		return nil, errors.New("not a replay store")
	}
	s := map[string]map[string]string{}
	return s, json.Unmarshal([]byte(j), &s)
}

func flagVal(args []string, name string) string {
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "--"+name+"="); ok {
			return v
		}
	}
	return ""
}

func (w *e2eWorld) migrator(c execx.Cmd, rest []string) (execx.Result, error) {
	w.advance(time.Second)
	ud := filepath.FromSlash(flagVal(rest, "user-data"))
	out := filepath.FromSlash(flagVal(rest, "out"))
	nonce := flagVal(rest, "nonce")
	store, err := readStoreAt(w.storePath(ud))
	if err != nil {
		return execx.Result{Code: 1, Output: "migrator: " + err.Error()}, nil
	}
	switch flagVal(rest, "mode") {
	case "dump":
		w.mu.Lock()
		w.dumps++
		n := w.dumps
		w.mu.Unlock()
		if w.failDumpAt > 0 && n == w.failDumpAt {
			return execx.Result{Code: 1, Output: "migrator: Local Storage could not be opened: IO error (synthetic)"}, nil
		}
		res := map[string]map[string]string{}
		for _, o := range strings.Split(flagVal(rest, "origins"), ",") {
			res[o] = store[o]
			if res[o] == nil {
				res[o] = map[string]string{}
			}
		}
		b, _ := json.Marshal(map[string]any{"nonce": nonce, "origins": res})
		writeFile(w.t, filepath.Join(out, "dump.json"), string(b))
		return execx.Result{}, nil
	case "write":
		w.mutate("settings write (copy)")
		if w.migrateFail == "write" {
			return execx.Result{Code: 1, Output: "migrator: LevelDB write failed: IO error (synthetic)"}, nil
		}
		in, err := os.ReadFile(filepath.FromSlash(flagVal(rest, "in")))
		if err != nil {
			return execx.Result{Code: 1, Output: err.Error()}, nil
		}
		writes := map[string]string{}
		if err := json.Unmarshal(in, &writes); err != nil {
			return execx.Result{Code: 1, Output: err.Error()}, nil
		}
		o := flagVal(rest, "origin")
		if store[o] == nil {
			store[o] = map[string]string{}
		}
		for k, v := range writes {
			store[o][k] = v
		}
		w.writeStoreAt(ud, store)
		b, _ := json.Marshal(map[string]any{"nonce": nonce, "written": len(writes), "mismatches": []string{}})
		writeFile(w.t, filepath.Join(out, "write.json"), string(b))
		return execx.Result{}, nil
	}
	return w.unexpected(c)
}

// ---- running the CLI ----

func (w *e2eWorld) environment() *environment {
	var stderr io.Writer = &w.errb
	if w.live != nil {
		stderr = io.MultiWriter(&w.errb, w.live)
	}
	return &environment{
		platform: w.plat,
		getenv:   func(string) string { return "" },
		stdout:   &w.out,
		stderr:   stderr,
		runner:   w,
		self:     filepath.Join(w.home, "bin", "hermes-safe-update.exe"),
		clock:    w.clock,
		selfPID:  w.selfPID,
		sleep:    w.sleep,
		apiBase:  w.api.URL,
		newUI: func(cfg *config.Config, out io.Writer) ui.Renderer {
			if w.vt && !cfg.Run.Plain {
				return ui.NewVT(out, w.plat, cfg.Theme, ui.Options{Clock: w.clock, NoTicker: w.pace == 0, Color: true})
			}
			p := ui.NewPlain(out, w.plat.Console)
			p.SetClock(w.clock)
			return p
		},
	}
}

func (w *e2eWorld) cli(args ...string) int {
	w.t.Helper()
	writeFile(w.t, filepath.Join(w.home, "safe-update.yaml"), w.yaml)
	w.con.terminal = w.vt
	w.code = run(context.Background(), args, w.environment())
	if n := w.con.left(); n > 0 {
		w.t.Errorf("%d scripted key(s) never asked for", n)
	}
	return w.code
}

// screen is what the run printed, with escape sequences removed (the
// assertions check text, not colours or cursor movement).
func (w *e2eWorld) screen() string { return w.shown(ansiRe.ReplaceAllString(w.errb.String(), "")) }

var countdownRe = regexp.MustCompile(`(?m)^  \d+s\n`)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)

func (w *e2eWorld) wantScreen(subs ...string) {
	w.t.Helper()
	s := w.screen()
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			w.t.Errorf("screen lacks %q\n---- screen ----\n%s", sub, s)
			return
		}
	}
}

func (w *e2eWorld) noScreen(subs ...string) {
	w.t.Helper()
	for _, sub := range subs {
		if strings.Contains(w.screen(), sub) {
			w.t.Errorf("screen unexpectedly has %q\n---- screen ----\n%s", sub, w.screen())
		}
	}
}

func (w *e2eWorld) wantCode(code int) {
	w.t.Helper()
	if w.code != code {
		w.t.Fatalf("exit %d, want %d\n---- screen ----\n%s", w.code, code, w.screen())
	}
}

// shown replaces the temp root with the synthetic user folder, so screens
// and transcripts read C:\Users\you\AppData\... and never name this machine.
// The VT renderer paints path segments, so an SGR sequence may sit between
// any two characters of the root: match those too.
func (w *e2eWorld) shown(s string) string {
	for _, r := range []string{w.root, filepath.ToSlash(w.root)} {
		var pat strings.Builder
		for i, c := range r {
			if i > 0 {
				pat.WriteString(`(?:\x1b\[[0-9;]*m)*`)
			}
			pat.WriteString(regexp.QuoteMeta(string(c)))
		}
		re := regexp.MustCompile(`(?i)` + pat.String() + `((?:` + sgrRe + `|[^\s\x1b"'<>|*?])*)`)
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			// The harness fakes a Windows home. On Unix the temp root is
			// followed by "/" separators (filepath.Join), which would read
			// C:\Users\you/AppData/...: turn the rest of the path into
			// backslashes so the goldens are the same on every OS.
			rest := re.FindStringSubmatch(m)[1]
			if filepath.Separator != '\\' {
				rest = strings.ReplaceAll(rest, "/", `\`)
			}
			return `C:\Users\you` + rest
		})
	}
	return s
}

// sgrRe is one SGR colour sequence (the VT renderer paints path segments).
const sgrRe = `\x1b\[[0-9;]*m`

// transcript compares the raw run output (escape sequences kept) with
// testdata/replay/<name>.ansi; -update rewrites it. Plain runs use .txt.
func (w *e2eWorld) transcript(name string) {
	w.t.Helper()
	ext := ".txt"
	if w.vt {
		ext = ".ansi"
	}
	got := w.shown(w.errb.String())
	if !w.vt {
		// the plain countdown ("  30s") is printed by a goroutine racing
		// the scripted keys: keep it out of the golden
		got = countdownRe.ReplaceAllString(got, "")
	}
	// privacy guard: the repo is public, no machine path may reach a transcript
	plain := ansiRe.ReplaceAllString(got, "")
	if home, _ := os.UserHomeDir(); home != "" && strings.Contains(strings.ToLower(plain), strings.ToLower(home)) {
		w.t.Fatalf("transcript %s leaks the real home directory", name)
	}
	if strings.Contains(plain, filepath.Base(w.root)) || strings.Contains(plain, os.TempDir()) {
		w.t.Fatalf("transcript %s leaks the temp dir", name)
	}
	if w.vt {
		// the demo console is 100 columns. Rows may wrap at the console
		// (python-behaviour 5.7), but the footer and the prompt must fit
		// (5.6, 5.12: rows-up redraw and the program-wrapped prompt).
		for _, l := range strings.FieldsFunc(plain, func(r rune) bool { return r == '\n' || r == '\r' }) {
			fixed := strings.Contains(l, "██") || strings.Contains(l, "── ") || strings.Contains(l, "► ") ||
				strings.Contains(l, "○ ") || strings.HasPrefix(strings.TrimSpace(l), "? ")
			if n := utf8.RuneCountInString(l); fixed && n > 100 {
				w.t.Errorf("transcript %s: %d-column row %q", name, n, l)
			}
		}
	}
	testutil.Golden(w.t, filepath.Join("replay", name+ext), []byte(got))
}

func (w *e2eWorld) store() map[string]map[string]string {
	s, err := readStoreAt(w.storePath(w.userData))
	if err != nil {
		w.t.Fatal(err)
	}
	return s
}
