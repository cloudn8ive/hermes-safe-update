package update

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/cua"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/gc"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/mirror"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
	"github.com/cloudn8ive/hermes-safe-update/internal/settings"
	"github.com/cloudn8ive/hermes-safe-update/internal/testutil"
	"github.com/cloudn8ive/hermes-safe-update/internal/timings"
	"github.com/cloudn8ive/hermes-safe-update/internal/ui"
)

// ---- fakes local to the flow tests ----

const (
	tHome     = `C:\Users\you\AppData\Local\hermes`
	tLauncher = tHome + `\bin\hermes.exe`
	tDesktop  = tHome + `\hermes-agent\apps\desktop\release\win-unpacked\Hermes.exe`
	oldSHA    = "1111111111111111111111111111111111111111"
	newSHA    = "2222222222222222222222222222222222222222"
)

type fakeLocator struct {
	in  hermes.Install
	err error
}

func (f fakeLocator) Locate(context.Context) (hermes.Install, error) { return f.in, f.err }

// fakeSessions returns script[i] on the i-th call, then the last entry forever.
type fakeSessions struct {
	mu     sync.Mutex
	script [][]hermes.Session
	calls  int
}

func (f *fakeSessions) Busy(context.Context, time.Duration) ([]hermes.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if len(f.script) == 0 {
		return nil, nil
	}
	i := min(f.calls-1, len(f.script)-1)
	return f.script[i], nil
}

type fakeGateway struct {
	mu      sync.Mutex
	state   hermes.GatewayState
	gate    hermes.GateResult
	probes  []hermes.GateResult // successive probe answers (then gate)
	stops   int
	starts  int
	stopErr error
	onStop  func()
	onStart func()
}

func (g *fakeGateway) State() hermes.GatewayState {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.state
}
func (g *fakeGateway) Probe(context.Context) (hermes.GateResult, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.probes) > 0 {
		r := g.probes[0]
		g.probes = g.probes[1:]
		return r, "probe"
	}
	return g.gate, "probe"
}
func (g *fakeGateway) Stop(context.Context) error {
	g.mu.Lock()
	g.stops++
	f := g.onStop
	g.mu.Unlock()
	if f != nil {
		f()
	}
	return g.stopErr
}
func (g *fakeGateway) Start(context.Context) error {
	g.mu.Lock()
	g.starts++
	f := g.onStart
	g.mu.Unlock()
	if f != nil {
		f()
	}
	return nil
}

type fakeRepo struct {
	mu   sync.Mutex
	head string
}

func (r *fakeRepo) Head(context.Context) (string, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.head[:7], r.head, nil
}
func (r *fakeRepo) DirtyCount(context.Context) (int, error) { return 0, nil }
func (r *fakeRepo) DiffKinds(context.Context) (*bool, *bool) {
	f := false
	return &f, &f
}
func (r *fakeRepo) set(h string) { r.mu.Lock(); r.head = h; r.mu.Unlock() }

type fakeVerifier struct {
	ok  bool
	out string
}

func (v fakeVerifier) VerifyDesktop(context.Context) (bool, string) { return v.ok, v.out }

type fakeMarker struct {
	mu       sync.Mutex
	holder   int
	stale    bool
	claimed  bool
	released bool
	claims   int
}

func (m *fakeMarker) Holder() (int, bool) { m.mu.Lock(); defer m.mu.Unlock(); return m.holder, m.stale }
func (m *fakeMarker) Claim() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.claimed = true
	m.claims++
	return nil
}
func (m *fakeMarker) Refresh() error { return nil }
func (m *fakeMarker) Release() (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.claimed {
		return false, nil
	}
	m.claimed, m.released = false, true
	return true, nil
}

type fakeSettings struct {
	settings.Service
	migrated int
	versions [][2]string // every SetHermesVersions call, in order
}

func (s *fakeSettings) SetHermesVersions(before, after string) {
	s.versions = append(s.versions, [2]string{before, after})
}

func (s *fakeSettings) Migrate(context.Context) (settings.Report, error) {
	s.migrated++
	return settings.Report{Line: "nothing to migrate"}, nil
}

type fakeGC struct{ runs []bool }

func (g *fakeGC) Run(_ context.Context, dry bool) (gc.Result, error) {
	g.runs = append(g.runs, dry)
	if dry {
		return gc.Result{Count: 2, MB: 300, Line: "2 old dependency set(s), ~300 MB to free",
			Lines: []string{"gen-a: REMOVE (~108 MB unique)", "gen-b: keep (selected)", "trash batches waiting: 1"}}, nil
	}
	return gc.Result{Count: 2, Line: "removed 2 old dependency set(s), freed 300 MB"}, nil
}

type fakeCUA struct{ refreshed int }

func (c *fakeCUA) Status(context.Context) cua.Status { return cua.Status{Text: "none (on-demand)"} }
func (c *fakeCUA) Refresh(context.Context, cua.Options) cua.Result {
	c.refreshed++
	return cua.Result{}
}

// ---- harness ----

type harness struct {
	t         *testing.T
	cfg       *config.Config
	plat      *platform.Platform
	procs     *platform.FakeProcs
	app       *platform.FakeApp
	con       *platform.FakeConsole
	clock     *testutil.Clock
	runner    *execx.Fake
	sessions  *fakeSessions
	gw        *fakeGateway
	repo      *fakeRepo
	realRepo  hermes.Repo // replaces repo when set (sandbox tests with real git)
	verifier  fakeVerifier
	marker    *fakeMarker
	settings  *fakeSettings
	gc        *fakeGC
	cua       *fakeCUA
	out       bytes.Buffer
	timings   string
	mode      Mode
	install   hermes.Install
	locErr    error
	slept     time.Duration
	updates   int
	update    func(n int) execx.Result // n = 1-based call count of `hermes update`
	updateCmd execx.Cmd                // the last `hermes update` Cmd
	mirror    mirror.Service
	ctx       context.Context
	wrap      execx.Runner      // replaces runner when set
	logSink   io.Writer         // receives the run's log (default: discarded)
	env       map[string]string // updater's environment (nil = empty)
	selfPID   int               // the updater's pid in the fake process table (0 = real)
}

func desktopProc() platform.Process {
	return platform.Process{PID: 100, Name: "Hermes.exe", Cmdline: `"` + tDesktop + `"`}
}
func gatewayProc() platform.Process {
	return platform.Process{PID: 200, Name: "python.exe", Cmdline: tHome + `\venv\python.exe -m hermes_cli.main gateway run`}
}
func backendProc() platform.Process {
	return platform.Process{PID: 300, Name: "hermes.exe", Cmdline: tLauncher + " serve --port 1"}
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	cfg := &config.Config{File: config.Defaults()}
	h := &harness{
		t:        t,
		cfg:      cfg,
		plat:     platform.NewFake(),
		clock:    testutil.NewClock(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)),
		runner:   &execx.Fake{},
		sessions: &fakeSessions{},
		gw:       &fakeGateway{state: hermes.GatewayState{CodeSHA: oldSHA, PID: 200}, gate: hermes.GateOK},
		repo:     &fakeRepo{head: oldSHA},
		verifier: fakeVerifier{ok: true},
		marker:   &fakeMarker{},
		settings: &fakeSettings{},
		gc:       &fakeGC{},
		cua:      &fakeCUA{},
		timings:  t.TempDir() + `/logs/safe-update-timings.json`,
		install: hermes.Install{Kind: hermes.KindCheckout, Home: tHome, Checkout: tHome + `\hermes-agent`,
			Launcher: tLauncher, Desktop: tDesktop},
	}
	h.procs = h.plat.Procs.(*platform.FakeProcs)
	h.app = h.plat.App.(*platform.FakeApp)
	h.con = h.plat.Console.(*platform.FakeConsole)
	h.gw.onStart = func() { h.procs.Add(gatewayProc()) }
	h.procs.Add(desktopProc())
	h.procs.Add(gatewayProc())
	// update available
	h.runner.On([]string{tLauncher, "update", "--check"}, execx.Result{Output: "3 commits behind origin/main"})
	// a successful `hermes update`: moves HEAD and the gateway to the new code
	h.onUpdate(0, "→ Fetching updates\nInstalling Python dependencies…\nBuilding desktop packaged app…\nUpdate complete! (v1.0 → v1.1)", true)
	h.runner.OnFunc([]string{tLauncher, "update", "--yes"}, func(c execx.Cmd) execx.Result {
		h.updates++
		h.updateCmd = c
		return h.update(h.updates)
	})
	return h
}

func (h *harness) advance() {
	h.repo.set(newSHA)
	h.gw.mu.Lock()
	h.gw.state.CodeSHA = newSHA
	h.gw.mu.Unlock()
}

// onUpdate scripts `hermes update`; advance moves HEAD + gateway to newSHA.
func (h *harness) onUpdate(rc int, out string, advance bool) {
	h.update = func(int) execx.Result {
		if advance {
			h.advance()
		}
		return execx.Result{Code: rc, Output: out}
	}
}

func (h *harness) run(flags ...string) error {
	for _, f := range flags {
		switch f {
		case "unattended":
			h.cfg.Run.Unattended = true
		case "yes":
			h.cfg.Run.Yes = true
		case "no-relaunch":
			h.cfg.Run.NoRelaunch = true
		case "force":
			h.cfg.Run.Force = true
		}
	}
	var repo hermes.Repo = h.repo
	if h.realRepo != nil {
		repo = h.realRepo
	}
	var runner execx.Runner = h.runner
	if h.wrap != nil {
		runner = h.wrap
	}
	d := Deps{
		Config:   h.cfg,
		Log:      h.logger(),
		Platform: h.plat,
		Runner:   runner,
		UI:       ui.NewPlain(&h.out, h.con),
		Clock:    h.clock,
		Sleep: func(ctx context.Context, dd time.Duration) error {
			h.slept += dd
			h.clock.Advance(dd)
			return ctx.Err()
		},
		Locator:  fakeLocator{in: h.install, err: h.locErr},
		Sessions: h.sessions,
		Gateway:  h.gw,
		Repo:     repo,
		Verifier: h.verifier,
		Settings: h.settings,
		GC:       h.gc,
		CUA:      h.cua,
		Hooks:    &ExecHooks{Runner: runner, Hooks: h.cfg.Hooks, Dir: tHome, Procs: h.plat.Procs},
		Mirror:   h.mirror,
		Marker:   h.marker,
		Classify: func(p platform.Process) string {
			return hermes.Classify(p, tHome, tHome+`\hermes-agent`, []string{"win-unpacked"})
		},
		FileExists:  func(string) bool { return true },
		Mode:        h.mode,
		SelfPID:     h.selfPID,
		Getenv:      func(k string) string { return h.env[k] },
		Title:       "Hermes Safe Update",
		TimingsPath: h.timings,
		LogPath:     tHome + `\logs\safe-update.log`,
	}
	r := NewRun(d)
	ctx := h.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return Execute(ctx, r, Flow(h.mode))
}

func (h *harness) logger() *slog.Logger {
	if h.logSink == nil {
		return nil
	}
	return slog.New(slog.NewTextHandler(h.logSink, nil))
}

func (h *harness) output() string { return h.out.String() }

func (h *harness) mustContain(subs ...string) {
	h.t.Helper()
	for _, s := range subs {
		if !strings.Contains(h.output(), s) {
			h.t.Errorf("output lacks %q\n--- output ---\n%s", s, h.output())
		}
	}
}

func key(r rune) platform.Key { return platform.Key{Rune: r} }

func busy(id string) []hermes.Session {
	return []hermes.Session{{Profile: "hermes", ID: id, Title: "a running turn", Idle: 10 * time.Second}}
}

// ---- acceptance tests (WORKPLAN I1b) ----

func TestIdleCloseUpdateVerifyOK(t *testing.T) {
	h := newHarness(t)
	h.con.Keys = []platform.Key{key('y')}
	err := h.run()
	if err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	calls := h.app.Calls()
	if len(calls) < 2 || calls[0] != "close 100" || calls[len(calls)-1] != "launch "+tDesktop {
		t.Errorf("app calls = %v", calls)
	}
	if h.updates != 1 || !h.marker.released || h.marker.claims != 1 {
		t.Errorf("updates=%d marker=%+v", h.updates, h.marker)
	}
	if h.settings.migrated != 1 || len(h.gc.runs) != 1 || h.gc.runs[0] {
		t.Errorf("post-update steps: settings=%d gc=%v", h.settings.migrated, h.gc.runs)
	}
	h.mustContain("RESULT: OK", "Hermes is updated and verified.", "v1.0 → v1.1")
	// the update ran with the exact argv and a visible window, no --gateway
	for _, c := range h.runner.Calls() {
		if len(c.Argv) > 1 && c.Argv[1] == "update" && c.Argv[2] == "--yes" {
			if strings.Join(c.Argv[1:], " ") != "update --yes --keep-stash --branch main" || !c.ShowWindow {
				t.Errorf("update argv = %v show=%v", c.Argv, c.ShowWindow)
			}
		}
	}
	hist, err := timings.LoadHistory(h.timings)
	if err != nil || len(hist) != 1 || hist[0].Kind != timings.KindUpdate || hist[0].OK == nil || !*hist[0].OK {
		t.Errorf("timings history = %+v err=%v", hist, err)
	}
}

func TestBusyWaitThenTimeoutCancels(t *testing.T) {
	h := newHarness(t)
	h.sessions.script = [][]hermes.Session{busy("s1")}
	err := h.run("unattended")
	if !errors.Is(err, apperr.ErrCancelled) {
		t.Fatalf("err = %v", err)
	}
	if apperr.ExitCode(err) != apperr.Cancelled {
		t.Errorf("exit %d", apperr.ExitCode(err))
	}
	if h.updates != 0 || len(h.app.Calls()) != 0 || h.marker.claims != 0 {
		t.Errorf("something changed: updates=%d app=%v marker=%+v", h.updates, h.app.Calls(), h.marker)
	}
	// D3: real elapsed time; gives up at idle_wait_max_s (1800 s)
	if h.slept < 1800*time.Second || h.slept > 1900*time.Second {
		t.Errorf("waited %v", h.slept)
	}
	h.mustContain("sessions still active after 30 min", "Update cancelled. Nothing was changed")
}

func TestBusyThenIdleProceeds(t *testing.T) {
	h := newHarness(t)
	h.sessions.script = [][]hermes.Session{busy("s1"), busy("s1"), nil}
	if err := h.run("unattended"); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if h.updates != 1 || h.slept < 120*time.Second {
		t.Errorf("updates=%d slept=%v", h.updates, h.slept)
	}
}

func TestAbortKeyCancels(t *testing.T) {
	h := newHarness(t)
	h.sessions.script = [][]hermes.Session{busy("s1")}
	h.con.Keys = []platform.Key{key('a')}
	err := h.run()
	if !errors.Is(err, apperr.ErrCancelled) || h.updates != 0 || len(h.app.Calls()) != 0 {
		t.Fatalf("err=%v updates=%d app=%v", err, h.updates, h.app.Calls())
	}
	h.mustContain("aborted by user")
}

func TestForceThenYesProceeds(t *testing.T) {
	h := newHarness(t)
	h.sessions.script = [][]hermes.Session{busy("s1")}
	// arrows are ignored (D3), then F, then Y
	h.con.Keys = []platform.Key{{Other: true}, key('f'), {Other: true}, key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if h.updates != 1 {
		t.Errorf("updates=%d", h.updates)
	}
	h.mustContain("close Hermes despite active sessions")
}

func TestConfirmTimeoutCancels(t *testing.T) {
	h := newHarness(t)
	h.cfg.Tunables.ConfirmTimeoutS = 1
	h.con.Keys = nil // nobody answers
	err := h.run()
	if !errors.Is(err, apperr.ErrCancelled) || h.updates != 0 || len(h.app.Calls()) != 0 {
		t.Fatalf("err=%v updates=%d app=%v", err, h.updates, h.app.Calls())
	}
	h.mustContain("no confirmation")
}

func TestYesSkipsConfirmButNotBusyCheck(t *testing.T) {
	h := newHarness(t)
	h.sessions.script = [][]hermes.Session{busy("s1")}
	h.con.Keys = []platform.Key{key('a')}
	err := h.run("yes")
	if !errors.Is(err, apperr.ErrCancelled) {
		t.Fatalf("--yes must not skip the busy prompt: err=%v", err)
	}
	h2 := newHarness(t)
	if err := h2.run("yes"); err != nil || h2.updates != 1 {
		t.Fatalf("--yes with idle sessions: err=%v updates=%d", err, h2.updates)
	}
}

func TestNoDesktopButGatewayStillAsks(t *testing.T) {
	h := newHarness(t)
	h.procs.Remove(100)
	h.sessions.script = [][]hermes.Session{busy("s1")}
	h.con.Keys = []platform.Key{key('a')}
	err := h.run()
	if !errors.Is(err, apperr.ErrCancelled) || h.sessions.calls == 0 {
		t.Fatalf("D2: must check sessions and ask: err=%v calls=%d", err, h.sessions.calls)
	}
}

func TestCloseIgnoredForceStillRunningCancels(t *testing.T) {
	h := newHarness(t)
	h.app.CloseBehaviour = platform.CloseIgnored
	h.app.Blocker = "a dialog was open"
	// the force kill does not remove it either: re-add on every list
	h.con.Keys = []platform.Key{key('y')}
	stuck := &stuckProcs{FakeProcs: h.procs, pid: 100}
	h.plat.Procs = stuck
	h.app.Procs = h.procs
	err := h.run()
	if !errors.Is(err, apperr.ErrCancelled) {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if h.updates != 0 || h.marker.claims != 0 {
		t.Errorf("updated without closing: updates=%d marker=%+v", h.updates, h.marker)
	}
	calls := h.app.Calls()
	if len(calls) < 2 || calls[0] != "close 100" || calls[1] != "force 100" {
		t.Errorf("calls %v", calls)
	}
	h.mustContain("did not close gracefully", "a dialog was open", "desktop app did not exit")
}

// stuckProcs keeps pid in every listing (a process that will not die).
type stuckProcs struct {
	*platform.FakeProcs
	pid int
}

func (s *stuckProcs) List(ctx context.Context) ([]platform.Process, error) {
	l, err := s.FakeProcs.List(ctx)
	for _, p := range l {
		if p.PID == s.pid {
			return l, err
		}
	}
	return append(l, desktopProc()), err
}
func (s *stuckProcs) Alive(pid int) bool { return pid == s.pid || s.FakeProcs.Alive(pid) }

func TestUpdateFailsRelaunchesAndReportsProblem(t *testing.T) {
	h := newHarness(t)
	h.onUpdate(1, "→ Fetching updates\nerror: npm build failed", false)
	h.con.Keys = []platform.Key{key('y')}
	err := h.run()
	if !errors.Is(err, apperr.ErrUpdateProblem) || apperr.ExitCode(err) != apperr.Failure {
		t.Fatalf("err = %v", err)
	}
	calls := h.app.Calls()
	if calls[len(calls)-1] != "launch "+tDesktop {
		t.Errorf("not relaunched: %v", calls)
	}
	if !h.marker.released {
		t.Error("marker not released")
	}
	if h.settings.migrated != 0 || len(h.gc.runs) != 0 {
		t.Errorf("post-update steps ran after a failed update: settings=%d gc=%v", h.settings.migrated, h.gc.runs)
	}
	h.mustContain("RESULT: PROBLEM", "The update needs attention")
}

func TestVerifyFailsHooksRunResultStaysProblem(t *testing.T) {
	h := newHarness(t)
	h.verifier = fakeVerifier{ok: false, out: "desktop build missing"}
	h.cfg.Hooks = []config.Hook{
		{Name: "tile", When: config.HookPostUpdate, Run: []string{"tile.exe"}},
		{Name: "notify", When: config.HookPostRelaunch, Run: []string{"notify.exe"}},
	}
	h.runner.On([]string{"tile.exe"}, execx.Result{Output: "tile ok"})
	h.runner.OnFunc([]string{"notify.exe"}, func(c execx.Cmd) execx.Result {
		if c.Env["HERMES_SAFE_UPDATE_RESULT"] != "problem" || c.Env["HERMES_SAFE_UPDATE_STAGE"] != "post-relaunch" {
			t.Errorf("hook env = %v", c.Env)
		}
		return execx.Result{Output: "sent\nnotified"}
	})
	h.con.Keys = []platform.Key{key('y')}
	err := h.run()
	if !errors.Is(err, apperr.ErrUpdateProblem) {
		t.Fatalf("err = %v", err)
	}
	ran := map[string]bool{}
	for _, c := range h.runner.Calls() {
		ran[c.Argv[0]] = true
	}
	if !ran["notify.exe"] || ran["tile.exe"] {
		t.Errorf("hooks ran = %v (post-relaunch yes, post-update no)", ran)
	}
	h.mustContain("RESULT: PROBLEM", "notified", "The update needs attention")
}

func TestHookTimeoutIsWarnOnly(t *testing.T) {
	h := newHarness(t)
	h.cfg.Hooks = []config.Hook{{Name: "slow", When: config.HookPostUpdate, Run: []string{"slow.exe"}, TimeoutS: 2}}
	h.runner.OnFunc([]string{"slow.exe"}, func(c execx.Cmd) execx.Result {
		if c.Timeout != 2*time.Second || c.Dir != tHome {
			t.Errorf("hook cmd = %+v", c)
		}
		return execx.Result{Code: execx.CodeTimeout, TimedOut: true, Output: "timed out after 2s"}
	})
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("a hook timeout must not fail the run: %v\n%s", err, h.output())
	}
	h.mustContain("hook slow timed out", "Hermes is updated and verified.")
}

func TestPauseGateBlockedStopsGatewayAndRetriesOnce(t *testing.T) {
	h := newHarness(t)
	h.gw.gate = hermes.GateOK
	h.gw.probes = []hermes.GateResult{hermes.GateOK} // pre-flight OK, then the real update hits the gate
	h.update = func(n int) execx.Result {
		if n == 1 {
			return execx.Result{Code: 1, Output: "RuntimeError: Could not map Windows gateway PIDs to profiles: no pid"}
		}
		h.advance()
		return execx.Result{Output: "→ Fetching updates\nUpdate complete! (v1 → v2)"}
	}
	h.gw.onStop = func() { h.procs.Remove(200) }
	h.con.Keys = []platform.Key{key('y')}
	err := h.run()
	if err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if h.updates != 2 || h.gw.stops != 1 || h.gw.starts == 0 {
		t.Errorf("updates=%d stops=%d starts=%d", h.updates, h.gw.stops, h.gw.starts)
	}
	h.mustContain("retried with the gateway stopped")
}

func TestPreflightGateBlockedStopsGatewayFirst(t *testing.T) {
	h := newHarness(t)
	h.gw.probes = []hermes.GateResult{hermes.GateBlocked, hermes.GateOK}
	h.gw.onStop = func() { h.procs.Remove(200) }
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if h.gw.stops != 1 || h.updates != 1 {
		t.Errorf("stops=%d updates=%d", h.gw.stops, h.updates)
	}
	h.mustContain("stopped before the update")
}

func TestMarkerHeldExits4(t *testing.T) {
	h := newHarness(t)
	h.marker.holder = 4242
	err := h.run()
	if !errors.Is(err, apperr.ErrPreflightStop) || apperr.ExitCode(err) != apperr.PreflightStop {
		t.Fatalf("err=%v", err)
	}
	if h.updates != 0 || len(h.app.Calls()) != 0 {
		t.Error("changed something")
	}
	h.mustContain("another update holds the marker (pid 4242)")
}

func TestLowDiskExits4(t *testing.T) {
	h := newHarness(t)
	h.plat.Disk.(*platform.FakeDisk).FreeBytes = 2e9
	err := h.run()
	if !errors.Is(err, apperr.ErrPreflightStop) || h.updates != 0 {
		t.Fatalf("err=%v updates=%d", err, h.updates)
	}
	h.mustContain("not enough free disk space")
}

func TestCheckFailedExits4(t *testing.T) {
	h := newHarness(t)
	h.runner = rebuild(h.runner, []string{tLauncher, "update", "--check"}, execx.Result{Code: 1, Output: "fatal: unable to access"})
	err := h.run()
	if !errors.Is(err, apperr.ErrPreflightStop) {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	h.mustContain("could not check for updates")
}

// rebuild returns a fresh Fake answering only argv (the update never runs).
func rebuild(_ *execx.Fake, argv []string, res execx.Result) *execx.Fake {
	f := &execx.Fake{}
	f.On(argv, res)
	return f
}

func TestUpToDateExits0(t *testing.T) {
	h := newHarness(t)
	h.runner = rebuild(h.runner, []string{tLauncher, "update", "--check"}, execx.Result{Output: "Already up to date."})
	if err := h.run(); err != nil {
		t.Fatalf("err=%v", err)
	}
	if h.updates != 0 || len(h.app.Calls()) != 0 {
		t.Error("changed something")
	}
	h.mustContain("Hermes is up to date")
}

func TestPackagedInstallExits5(t *testing.T) {
	h := newHarness(t)
	h.install = hermes.Install{Kind: hermes.KindMSIX, StepAside: "updates come from the App Installer feed"}
	err := h.run()
	if apperr.ExitCode(err) != apperr.NotCheckout {
		t.Fatalf("err=%v", err)
	}
	h.mustContain("App Installer feed")
}

func TestLauncherMissingExits2(t *testing.T) {
	h := newHarness(t)
	h.locErr = apperr.ErrLauncherMissing
	err := h.run()
	if apperr.ExitCode(err) != apperr.LauncherMissing {
		t.Fatalf("err=%v", err)
	}
}

func TestCheckNeverStopsAnything(t *testing.T) {
	h := newHarness(t)
	h.mode = ModeCheck
	h.procs.Add(backendProc())
	h.sessions.script = [][]hermes.Session{busy("s1")}
	if err := h.run(); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	if len(h.app.Calls()) != 0 || len(h.procs.Killed()) != 0 || h.updates != 0 || h.marker.claims != 0 || h.gw.stops != 0 {
		t.Errorf("check changed something: app=%v killed=%v updates=%d", h.app.Calls(), h.procs.Killed(), h.updates)
	}
	if len(h.gc.runs) != 1 || !h.gc.runs[0] {
		t.Errorf("gc preview = %v", h.gc.runs)
	}
	h.mustContain("  gen-a: REMOVE (~108 MB unique)", "  gen-b: keep (selected)", "  trash batches waiting: 1")
	h.mustContain("Update available. Hermes was not touched.", "3 new commit(s)", "1 active in the last 3 min")
	hist, _ := timings.LoadHistory(h.timings)
	if len(hist) != 1 || hist[0].Kind != timings.KindCheck {
		t.Errorf("history = %+v", hist)
	}
}

func TestTimingsSavedInPlainMode(t *testing.T) {
	h := newHarness(t)
	h.cfg.Run.Plain = true
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if hist, _ := timings.LoadHistory(h.timings); len(hist) != 1 {
		t.Errorf("D5: plain mode must save timings, got %d records", len(hist))
	}
}

func TestLeftoverBackendsStoppedAfterClose(t *testing.T) {
	h := newHarness(t)
	h.procs.Add(backendProc())
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	k := h.procs.Killed()
	if len(k) != 1 || k[0] != 300 {
		t.Errorf("killed %v, want the backend 300 only", k)
	}
}

// SR-1: processes that merely mention a Hermes file or path (an editor, grep,
// another install's app) are not Hermes and must never reach KillTree or the
// desktop close/force-close.
func TestLookalikeProcessesAreNeverKilled(t *testing.T) {
	h := newHarness(t)
	h.procs.Add(backendProc())
	look := []platform.Process{
		{PID: 810, Name: "notepad.exe", Cmdline: `notepad.exe ` + tHome + `\x\hermes_kernel_runner.py`},
		{PID: 811, Name: "rg.exe", Cmdline: `rg hermes_kernel_runner.py`},
		{PID: 812, Name: "Code.exe", Cmdline: `Code.exe ` + tHome + `\hermes-agent\hermes_cli serve notes.md`},
		{PID: 813, Name: "python.exe", Cmdline: `python.exe C:\elsewhere\hermes_kernel_runner.py`},
		{PID: 814, Name: "Hermes.exe", Exe: `D:\other\apps\desktop\release\win-unpacked\Hermes.exe`,
			Cmdline: `"D:\other\apps\desktop\release\win-unpacked\Hermes.exe"`},
	}
	for _, p := range look {
		h.procs.Add(p)
	}
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if k := h.procs.Killed(); len(k) != 1 || k[0] != 300 {
		t.Errorf("killed %v, want the backend 300 only (lookalikes spared)", k)
	}
	for _, c := range h.app.Calls() {
		for _, p := range look {
			if strings.Contains(c, strconv.Itoa(p.PID)) {
				t.Errorf("app call %q touched lookalike pid %d", c, p.PID)
			}
		}
	}
}

func TestNoRelaunchFlag(t *testing.T) {
	h := newHarness(t)
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run("no-relaunch"); err != nil {
		t.Fatal(err)
	}
	for _, c := range h.app.Calls() {
		if strings.HasPrefix(c, "launch") {
			t.Errorf("relaunched with --no-relaunch: %v", h.app.Calls())
		}
	}
}

func TestInterruptDuringUpdateIsProblemNotCancel(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	h.update = func(int) execx.Result { cancel(); return execx.Result{Code: 1, Output: "→ Fetching updates"} }
	h.con.Keys = []platform.Key{key('y')}
	h.ctx = ctx
	err := h.run()
	if apperr.ExitCode(err) != apperr.Failure {
		t.Fatalf("exit %d (%v): something changed, so it is not 'cancelled'", apperr.ExitCode(err), err)
	}
	if !h.marker.released || h.app.Calls()[len(h.app.Calls())-1] != "launch "+tDesktop {
		t.Errorf("finally steps: marker=%+v app=%v", h.marker, h.app.Calls())
	}
}

type fakeMirror struct {
	mirror.Service
	st       mirror.Status
	declined bool
}

func (m *fakeMirror) Status() mirror.Status { return m.st }
func (m *fakeMirror) Seed(context.Context, string) mirror.SeedResult {
	return mirror.SeedResult{Reason: "no mirror set up"}
}
func (m *fakeMirror) APICompare(context.Context, string) *mirror.Compare { return nil }
func (m *fakeMirror) Location(context.Context) (string, float64, string) {
	return `X:\hermes-mirror\hermes-agent.git`, 1.5, "room there"
}
func (m *fakeMirror) Decline() error { m.declined = true; return nil }

func TestMirrorOfferOnlyOnARealConsole(t *testing.T) {
	h := newHarness(t)
	h.mode = ModeCheck
	h.mirror = &fakeMirror{}
	h.con.Terminal = false
	start := time.Now()
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.output(), "Press S") || time.Since(start) > 5*time.Second {
		t.Errorf("offered on a non-console:\n%s", h.output())
	}
	h2 := newHarness(t)
	h2.mode = ModeCheck
	m := &fakeMirror{}
	h2.mirror = m
	h2.con.Terminal = true
	h2.con.Keys = []platform.Key{key('n')}
	if err := h2.run(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h2.output(), "Press S") || !m.declined {
		t.Errorf("console: offer not shown/declined:\n%s", h2.output())
	}
}

// ctxProbe records whether the context given to `hermes update` was
// cancelled by a Ctrl+C in the parent.
type ctxProbe struct {
	*execx.Fake
	cancel     func()
	childSawIt bool
}

func (c *ctxProbe) Stream(ctx context.Context, cmd execx.Cmd, on func(string)) (execx.Result, error) {
	if len(cmd.Argv) > 2 && cmd.Argv[1] == "update" && cmd.Argv[2] == "--yes" {
		c.cancel() // Ctrl+C arrives while the update runs
		c.childSawIt = ctx.Err() != nil
	}
	return c.Fake.Stream(ctx, cmd, on)
}

func TestCtrlCDuringUpdateDoesNotKillTheUpdater(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	h.ctx = ctx
	probe := &ctxProbe{Fake: h.runner, cancel: cancel}
	h.wrap = probe
	h.con.Keys = []platform.Key{key('y')}
	err := h.run()
	if probe.childSawIt {
		t.Error("the update's context was cancelled: execx would hard-kill `hermes update`; it must get the console's Ctrl+C itself")
	}
	if apperr.ExitCode(err) != apperr.Failure {
		t.Errorf("exit %d (%v)", apperr.ExitCode(err), err)
	}
	h.mustContain("interrupted")
}

// On the 90-min timeout the whole `hermes update` tree must go (Python
// kill_tree): the runner's cancel is wired to Platform.Procs.KillTree on
// the update child's pid. The fake runner simulates the timeout by calling
// the cancel hook itself.
func TestUpdateTimeoutKillsTheUpdateTree(t *testing.T) {
	h := newHarness(t)
	const updatePid = 4242
	h.procs.Add(platform.Process{PID: updatePid, Name: "hermes.exe", Cmdline: tLauncher + " update --yes"})
	var hookErr error
	hooked := false
	h.update = func(int) execx.Result {
		if c := h.updateCmd; c.KillTree == nil {
			t.Error("hermes update runs without a tree-kill cancel: a timeout would orphan its children")
		} else {
			hooked = true
			hookErr = c.KillTree(updatePid)
		}
		return execx.Result{Code: execx.CodeTimeout, TimedOut: true, Output: "→ Fetching updates\ntimed out after 1h30m0s"}
	}
	h.con.Keys = []platform.Key{key('y')}
	err := h.run()
	if !hooked {
		t.FailNow()
	}
	if hookErr != nil {
		t.Errorf("KillTree hook: %v", hookErr)
	}
	killed := h.procs.Killed()
	found := false
	for _, pid := range killed {
		found = found || pid == updatePid
	}
	if !found {
		t.Errorf("KillTree(%d) not called; killed = %v", updatePid, killed)
	}
	if apperr.ExitCode(err) != apperr.Failure {
		t.Errorf("exit %d (%v)", apperr.ExitCode(err), err)
	}
	h.mustContain("RESULT: PROBLEM")
}

// Brief ("Generic core + personal config"): post-update hooks run after a
// verified update with Hermes still closed, before the relaunch;
// post-relaunch hooks run after it.
func TestPostUpdateHookRunsBeforeRelaunchPostRelaunchAfter(t *testing.T) {
	h := newHarness(t)
	h.cfg.Hooks = []config.Hook{
		{Name: "tile", When: config.HookPostUpdate, Run: []string{"tile.exe"}},
		{Name: "notify", When: config.HookPostRelaunch, Run: []string{"notify.exe"}},
	}
	launched := func() bool {
		for _, c := range h.app.Calls() {
			if strings.HasPrefix(c, "launch ") {
				return true
			}
		}
		return false
	}
	var tileAfterLaunch, notifyAfterLaunch *bool
	rec := func(dst **bool) func(execx.Cmd) execx.Result {
		return func(execx.Cmd) execx.Result {
			v := launched()
			*dst = &v
			return execx.Result{Output: "ok"}
		}
	}
	h.runner.OnFunc([]string{"tile.exe"}, rec(&tileAfterLaunch))
	h.runner.OnFunc([]string{"notify.exe"}, rec(&notifyAfterLaunch))
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if tileAfterLaunch == nil || notifyAfterLaunch == nil {
		t.Fatalf("hooks did not both run: tile=%v notify=%v", tileAfterLaunch, notifyAfterLaunch)
	}
	if *tileAfterLaunch {
		t.Error("post-update hook ran after Hermes was relaunched; Hermes must still be closed")
	}
	if !*notifyAfterLaunch {
		t.Error("post-relaunch hook ran before Hermes was relaunched")
	}
}

// The log file is pasted into bug reports: busy-session ids and titles stay
// on the screen (the user wants to see what is busy) and never reach it.
func TestSessionIDsAndTitlesStayOutOfTheLog(t *testing.T) {
	const secretID, secretTitle = "sess-7f3a9c-private", "Draft my divorce letter"
	for _, mode := range []Mode{ModeUpdate, ModeCheck} {
		h := newHarness(t)
		h.mode = mode
		h.sessions.script = [][]hermes.Session{
			{{Profile: "hermes", ID: secretID, Title: secretTitle, Idle: 42 * time.Second}},
			nil,
		}
		var logBuf bytes.Buffer
		h.logSink = &logBuf
		h.con.Keys = []platform.Key{key('y')}
		if err := h.run("unattended"); err != nil {
			t.Fatalf("mode %v: err = %v\n%s", mode, err, h.output())
		}
		h.mustContain(secretID, secretTitle) // still on screen
		if got := logBuf.String(); strings.Contains(got, secretID) || strings.Contains(got, secretTitle) {
			t.Errorf("mode %v: log leaks a session id or title:\n%s", mode, got)
		}
		if !strings.Contains(logBuf.String(), "session 1 (busy, last activity 42s ago)") {
			t.Errorf("mode %v: log lacks the anonymous session line:\n%s", mode, logBuf.String())
		}
	}
}

// The mirror folder is the user's choice and may be personal: the summary
// card and the offer prompt show it on the screen; the log does not get it.
func TestMirrorFolderStaysOutOfTheLog(t *testing.T) {
	const folder = `Q:\private-place\hermes-agent.git`
	for _, ok := range []bool{true, false} {
		h := newHarness(t)
		h.mode = ModeCheck
		fm := &fakeMirror{}
		if ok {
			fm.st = mirror.Status{OK: true, ConfiguredPath: folder}
		}
		h.mirror = fm
		h.con.Terminal = true
		h.con.Keys = []platform.Key{key('n')}
		var logBuf bytes.Buffer
		h.logSink = &logBuf
		if err := h.run(); err != nil {
			t.Fatalf("err = %v\n%s", err, h.output())
		}
		if strings.Contains(logBuf.String(), "private-place") || strings.Contains(logBuf.String(), "hermes-mirror") {
			t.Errorf("mirror ok=%v: log carries a mirror folder:\n%s", ok, logBuf.String())
		}
		if ok && !strings.Contains(h.output(), folder) {
			t.Errorf("screen lost the mirror folder:\n%s", h.output())
		}
	}
}

// The settings backup manifest records the Hermes version read in
// pre-flight (before) and the one read after the update (after).
func TestSettingsManifestGetsBeforeAndAfterVersions(t *testing.T) {
	h := newHarness(t)
	n := 0
	h.runner.OnFunc([]string{tLauncher, "--version"}, func(execx.Cmd) execx.Result {
		n++
		v := "v1.0"
		if h.updates > 0 {
			v = "v1.1"
		}
		return execx.Result{Output: "Hermes Agent " + v + " (2026.10.1)\nInstall directory: " + h.install.Checkout + "\n"}
	})
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if h.settings.migrated != 1 {
		t.Fatalf("migrated = %d", h.settings.migrated)
	}
	got := h.settings.versions
	if len(got) == 0 || got[len(got)-1] != [2]string{"v1.0", "v1.1"} {
		t.Errorf("SetHermesVersions calls = %v, want last = [v1.0 v1.1]", got)
	}
}

func TestVersionValueGatewayNoteOnlyWhenDifferent(t *testing.T) {
	if got := versionValue("4e7403130ee", "4e7403130e"); got != "4e7403130ee" {
		t.Errorf("equal: %q", got)
	}
	if got := versionValue("4e7403130ee", "abcdef0123"); got != "4e7403130ee (gateway on abcdef0123)" {
		t.Errorf("differing: %q", got)
	}
}

// chatChain puts the updater (pid 900) under a shell (pid 800) whose parent is
// parent, and makes the fake table contain them.
func (h *harness) chatChain(parent platform.Process) {
	h.procs.Add(parent)
	h.procs.Add(platform.Process{PID: 800, PPID: parent.PID, Name: "cmd.exe", Created: h.clock.Now()})
	h.procs.Add(platform.Process{PID: 900, PPID: 800, Name: "hermes-safe-update.exe", Created: h.clock.Now()})
	h.selfPID = 900
}

func TestStartedFromGatewayChatStopsBeforeClosing(t *testing.T) {
	for name, parent := range map[string]platform.Process{
		"gateway": gatewayProc(),
		"backend": backendProc(),
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.chatChain(parent)
			err := h.run("yes")
			if !errors.Is(err, apperr.ErrPreflightStop) || apperr.ExitCode(err) != apperr.PreflightStop {
				t.Fatalf("err=%v", err)
			}
			if h.updates != 0 || len(h.app.Calls()) != 0 || h.gw.stops != 0 {
				t.Errorf("changed something: updates=%d app=%v", h.updates, h.app.Calls())
			}
			h.mustContain("Run this from a normal terminal or the Start menu, not from inside a Hermes chat")
		})
	}
}

func TestStartedFromPlainTerminalContinues(t *testing.T) {
	h := newHarness(t)
	h.chatChain(platform.Process{PID: 700, Name: "explorer.exe"})
	if err := h.run("yes"); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	if h.updates != 1 {
		t.Errorf("updates=%d", h.updates)
	}
}

func TestChatAncestorIgnoresReusedPid(t *testing.T) {
	h := newHarness(t)
	gw := gatewayProc()
	gw.Created = h.clock.Now().Add(time.Hour) // newer than its "child": pid reuse
	h.chatChain(gw)
	if err := h.run("yes"); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
}

func TestCheckWithGatewayAncestorStillWorks(t *testing.T) {
	h := newHarness(t)
	h.mode = ModeCheck
	h.chatChain(gatewayProc())
	if err := h.run(); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	if strings.Contains(h.output(), "inside a Hermes chat") {
		t.Error("check refused")
	}
}

// brokenChain leaves the updater's parent chain ending at a pid that is not in
// the snapshot (an intermediate shell exited), so the ancestor walk finds nothing.
func (h *harness) brokenChain() {
	h.procs.Add(platform.Process{PID: 900, PPID: 3060, Name: "hermes-safe-update.exe", Created: h.clock.Now()})
	h.selfPID = 900
}

func TestBrokenChainWithSessionEnvStops(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"session id":   {"HERMES_SESSION_ID": "20260101_000000_aaaaaa", "HERMES_DESKTOP": "1"},
		"agent marker": {"HERMES_AGENT": "true"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.brokenChain()
			h.env = env
			err := h.run("yes")
			if !errors.Is(err, apperr.ErrPreflightStop) || apperr.ExitCode(err) != apperr.PreflightStop {
				t.Fatalf("err=%v", err)
			}
			if h.updates != 0 || len(h.app.Calls()) != 0 || h.gw.stops != 0 {
				t.Errorf("changed something: updates=%d app=%v", h.updates, h.app.Calls())
			}
			h.mustContain("Run this from a normal terminal or the Start menu, not from inside a Hermes chat")
		})
	}
}

func TestBrokenChainWithoutHermesEnvContinues(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"empty":           nil,
		"start menu-like": {"HERMES_DESKTOP": "1", "HERMES_HOME": "", "HERMES_AGENT": "", "HERMES_SESSION_ID": " "},
		"agent not true":  {"HERMES_AGENT": "false"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.brokenChain()
			h.env = env
			if err := h.run("yes"); err != nil {
				t.Fatalf("err=%v\n%s", err, h.output())
			}
			if h.updates != 1 {
				t.Errorf("updates=%d", h.updates)
			}
		})
	}
}

func TestCheckWithChatEnvStillWorks(t *testing.T) {
	h := newHarness(t)
	h.mode = ModeCheck
	h.brokenChain()
	h.env = map[string]string{"HERMES_SESSION_ID": "x", "HERMES_AGENT": "true"}
	if err := h.run(); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	if strings.Contains(h.output(), "inside a Hermes chat") {
		t.Error("check refused")
	}
}
