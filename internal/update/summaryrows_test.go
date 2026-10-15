package update

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/mirror"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// cardRow returns the value of a summary card row ("" when there is none).
func cardRow(out, key string) string {
	m := regexp.MustCompile(`(?m)^\s{2}` + regexp.QuoteMeta(key) + `\s{2,}(.*?)\s*$`).FindStringSubmatch(out)
	if m == nil {
		return ""
	}
	return m[1]
}

func launched(h *harness) bool {
	for _, c := range h.app.Calls() {
		if strings.HasPrefix(c, "launch") {
			return true
		}
	}
	return false
}

// ---- Version row: the commits that were really installed ----

func TestVersionCountsTheInstalledCommitsNotThePreflightForecast(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		gitN    int
		gitOK   bool
		want    string
		wantGit bool // CommitCount must have been asked
	}{
		{"git wins over the Found line and the forecast", "→ Found 7 new commit(s)\nUpdate complete! (v1.0 → v1.1)", 9, true, "v1.0 → v1.1  (9 commits)", true},
		{"git cannot say: the Found line", "→ Found 7 new commit(s)\nUpdate complete! (v1.0 → v1.1)", 0, false, "v1.0 → v1.1  (7 commits)", true},
		{"neither: the pre-flight number", "Update complete! (v1.0 → v1.1)", 0, false, "v1.0 → v1.1  (3 commits)", true},
		{"git says fewer than the forecast", "Update complete! (v1.0 → v1.1)", 2, true, "v1.0 → v1.1  (2 commits)", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.repo.count, h.repo.countOK = c.gitN, c.gitOK
			h.onUpdate(0, c.out, true)
			h.con.Keys = []platform.Key{key('y')}
			if err := h.run(); err != nil {
				t.Fatal(err)
			}
			if got := cardRow(h.output(), "Version"); got != c.want {
				t.Errorf("Version = %q, want %q\n%s", got, c.want, h.output())
			}
			// asked about exactly the commits between the two real HEADs: how
			// many came in, and whether the old HEAD is still all in the new one
			if len(h.repo.counted) != 2 || h.repo.counted[0] != [2]string{oldSHA, newSHA} || h.repo.counted[1] != [2]string{newSHA, oldSHA} {
				t.Errorf("CommitCount calls = %v", h.repo.counted)
			}
		})
	}
}

func TestVersionCountSkipsGitWhenHeadDidNotMove(t *testing.T) {
	h := newHarness(t)
	h.onUpdate(0, "→ Found 4 new commit(s)\nUpdate complete! (v1.0 → v1.0)", false)
	h.gw.state.CodeSHA = oldSHA
	h.con.Keys = []platform.Key{key('y')}
	_ = h.run()
	if len(h.repo.counted) != 0 {
		t.Errorf("asked git about an empty range: %v", h.repo.counted)
	}
	if got := cardRow(h.output(), "Version"); got != "v1.0 → v1.0  (4 commits)" {
		t.Errorf("Version = %q", got)
	}
}

// ---- Source row: how fresh the mirror was when the update started ----

func mirrorHarness(t *testing.T, seed mirror.SeedResult, before, after mirror.Status) (*harness, *fakeMirror) {
	t.Helper()
	h := newHarness(t)
	n := 6
	seed.Commits = &n
	if seed.Reason == "" {
		seed.Reason = "ok"
	}
	m := &fakeMirror{st: before, stAfterSeed: &after, seed: &seed}
	h.mirror = m
	h.con.Keys = []platform.Key{key('y')}
	return h, m
}

func TestSourceRowAfterARefreshInThisRun(t *testing.T) {
	// pre-flight Status said "6 h ago"; the seed refreshed the mirror seconds
	// before the check, and the card is drawn ten minutes later
	t0 := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	stale := mirror.Status{OK: true, LastOK: t0.Add(-6 * time.Hour)}
	fresh := mirror.Status{OK: true, LastOK: t0}
	h, _ := mirrorHarness(t, mirror.SeedResult{Used: true, Refreshed: true, Current: true}, stale, fresh)
	h.update = func(int) execx.Result {
		h.clock.Advance(10 * time.Minute)
		h.advance()
		return execx.Result{Output: "Update complete! (v1.0 → v1.1)"}
	}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	got := cardRow(h.output(), "Source")
	if got != "local mirror, refreshed at the start of this run" {
		t.Errorf("Source = %q\n%s", got, h.output())
	}
	if strings.Contains(h.output(), "6 h") {
		t.Errorf("the stale pre-flight age is still shown:\n%s", h.output())
	}
}

func TestSourceRowMirrorThatAlreadyMatchedGitHub(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	old := mirror.Status{OK: true, LastOK: t0.Add(-6 * time.Hour)}
	h, _ := mirrorHarness(t, mirror.SeedResult{Used: true, Current: true}, old, old)
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	got := cardRow(h.output(), "Source")
	if got != "local mirror, up to date with GitHub at the start of this run" {
		t.Errorf("Source = %q", got)
	}
}

func TestSourceRowUnprovenMirrorAgeIsMeasuredAtTheCheck(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	// last refresh 40 min before the run (the pre-flight Status still said
	// 6 h: the seed refreshed in between); the update then takes 30 minutes
	st := mirror.Status{OK: true, LastOK: t0.Add(-40 * time.Minute)}
	stale := mirror.Status{OK: true, LastOK: t0.Add(-6 * time.Hour)}
	h, _ := mirrorHarness(t, mirror.SeedResult{Used: true}, stale, st)
	h.update = func(int) execx.Result {
		h.clock.Advance(30 * time.Minute)
		h.advance()
		return execx.Result{Output: "Update complete! (v1.0 → v1.1)"}
	}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if got := cardRow(h.output(), "Source"); got != "local mirror, refreshed 40 min before this run" {
		t.Errorf("Source = %q (the card is drawn 30 min later; the age must not grow)", got)
	}
	// and a mirror that was never refreshed says so
	h2, _ := mirrorHarness(t, mirror.SeedResult{Used: true}, mirror.Status{OK: true}, mirror.Status{OK: true})
	if err := h2.run(); err != nil {
		t.Fatal(err)
	}
	if got := cardRow(h2.output(), "Source"); got != "local mirror, never refreshed" {
		t.Errorf("Source = %q", got)
	}
}

func TestSourceRowMirrorRefreshFailedNeverClaimsAFreshMirror(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	st := mirror.Status{OK: true, LastOK: t0.Add(-6 * time.Hour)}
	h := newHarness(t)
	h.mirror = &fakeMirror{st: st, seed: &mirror.SeedResult{Reason: "mirror refresh failed (git fetch rc=128: unable to access)"}}
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	got := cardRow(h.output(), "Source")
	if strings.Contains(got, "mirror") || strings.Contains(got, "refreshed") || got != "GitHub (git fetch)" {
		t.Errorf("Source = %q: the update did not come from the mirror", got)
	}
}

// ---- Newer row: main moved while the update ran ----

func newerHarness(t *testing.T, n mirror.Newer, ok bool) (*harness, *fakeMirror) {
	t.Helper()
	h := newHarness(t)
	m := &fakeMirror{newer: n, newerOK: ok}
	h.mirror = m
	h.con.Keys = []platform.Key{key('y')}
	return h, m
}

func ptrInt(n int) *int { return &n }

func TestNewerRowTexts(t *testing.T) {
	const tail = " landed on GitHub while this ran; run Safe Update again when convenient"
	cases := []struct {
		name string
		n    mirror.Newer
		want string
	}{
		{"several", mirror.Newer{Ahead: true, Commits: ptrInt(7)}, "7 more commits" + tail},
		{"one", mirror.Newer{Ahead: true, Commits: ptrInt(1)}, "1 more commit" + tail},
		{"only a sha comparison", mirror.Newer{Ahead: true}, "Newer commits" + tail},
		{"a zero count is no news", mirror.Newer{Ahead: true, Commits: ptrInt(0)}, "Newer commits" + tail},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, m := newerHarness(t, c.n, true)
			if err := h.run(); err != nil {
				t.Fatalf("a newer commit must never fail the run: %v", err)
			}
			if got := cardRow(h.output(), "Newer"); got != c.want {
				t.Errorf("Newer = %q, want %q\n%s", got, c.want, h.output())
			}
			// asked about the HEAD that was installed, in the real checkout
			if m.newerCalls != 1 || m.newerHead != newSHA || m.newerCheckout != h.install.Checkout {
				t.Errorf("NewerThan(%q, %q) x%d", m.newerCheckout, m.newerHead, m.newerCalls)
			}
			if !m.newerDeadline {
				t.Error("the look had no short deadline")
			}
		})
	}
}

func TestNewerRowOmittedWhenNothingToSay(t *testing.T) {
	cases := []struct {
		name string
		n    mirror.Newer
		ok   bool
	}{
		{"API and git both failed", mirror.Newer{}, false},
		{"main did not move", mirror.Newer{Ahead: false, Commits: ptrInt(0)}, true},
		{"failure that still carries a stale answer", mirror.Newer{Ahead: true, Commits: ptrInt(5)}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, m := newerHarness(t, c.n, c.ok)
			if err := h.run(); err != nil {
				t.Fatalf("err = %v", err)
			}
			if got := cardRow(h.output(), "Newer"); got != "" {
				t.Errorf("row shown: %q", got)
			}
			if m.newerCalls != 1 {
				t.Errorf("NewerThan called %d times", m.newerCalls)
			}
			h.mustContain("Hermes is updated and verified.")
		})
	}
}

func TestNewerRowNeverRunsForAnUnverifiedUpdateOrACheck(t *testing.T) {
	h, m := newerHarness(t, mirror.Newer{Ahead: true, Commits: ptrInt(4)}, true)
	h.verifier.ok = false
	err := h.run()
	if !errors.Is(err, apperr.ErrUpdateProblem) {
		t.Fatalf("want the problem exit, got %v", err)
	}
	if m.newerCalls != 0 || cardRow(h.output(), "Newer") != "" {
		t.Errorf("unverified update: calls=%d row=%q", m.newerCalls, cardRow(h.output(), "Newer"))
	}

	h2, m2 := newerHarness(t, mirror.Newer{Ahead: true, Commits: ptrInt(4)}, true)
	h2.mode = ModeCheck
	if err := h2.run(); err != nil {
		t.Fatal(err)
	}
	if m2.newerCalls != 0 || strings.Contains(h2.output(), "landed on GitHub") {
		t.Errorf("--check asked: calls=%d", m2.newerCalls)
	}
}

func TestNewerRowWithoutMirrorServiceIsOmitted(t *testing.T) {
	h := newHarness(t) // no mirror service wired at all
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if got := cardRow(h.output(), "Newer"); got != "" {
		t.Errorf("row shown: %q", got)
	}
}

// slowMirror blocks in NewerThan until its context is done.
type slowMirror struct {
	*fakeMirror
	waited time.Duration
}

func (s *slowMirror) NewerThan(ctx context.Context, _, _ string) (mirror.Newer, bool) {
	start := time.Now()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second): // a missing deadline fails the test instead of hanging it
	}
	s.waited = time.Since(start)
	return mirror.Newer{Ahead: true, Commits: ptrInt(9)}, true // late answers are ignored by the caller's ok
}

func TestNewerLookIsBoundedAndNeverDelaysTheCardForLong(t *testing.T) {
	old := newerBudget
	newerBudget = 80 * time.Millisecond
	t.Cleanup(func() { newerBudget = old })
	h := newHarness(t)
	sm := &slowMirror{fakeMirror: &fakeMirror{}}
	h.mirror = sm
	h.con.Keys = []platform.Key{key('y')}
	start := time.Now()
	if err := h.run(); err != nil {
		t.Fatalf("err = %v", err)
	}
	if sm.waited > time.Second || sm.waited < 50*time.Millisecond {
		t.Errorf("run %v, waited %v: the deadline was not applied", time.Since(start), sm.waited)
	}
	if old > 10*time.Second {
		t.Errorf("the production budget is %v", old)
	}
}

// ---- desktop.relaunch ----

func (h *harness) closeAtStart() { h.procs.Remove(100) } // the user closed Hermes just before

func TestRelaunchAutoOpenAppIsReopenedAsBefore(t *testing.T) {
	h := newHarness(t)
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if !launched(h) {
		t.Errorf("not reopened: %v", h.app.Calls())
	}
	if got := cardRow(h.output(), "Hermes app"); got != "" {
		t.Errorf("row shown when the app was reopened: %q", got)
	}
}

func TestRelaunchAutoClosedAtStartStaysClosedAndSaysWhy(t *testing.T) {
	h := newHarness(t)
	h.closeAtStart()
	if err := h.run("yes"); err != nil {
		t.Fatal(err)
	}
	if launched(h) {
		t.Errorf("reopened an app that was closed at the start: %v", h.app.Calls())
	}
	want := "not reopened (it was closed when the update started; set desktop.relaunch: always to reopen it)"
	if got := cardRow(h.output(), "Hermes app"); got != want {
		t.Errorf("Hermes app = %q, want %q", got, want)
	}
}

func TestRelaunchAlwaysReopensAnAppThatWasClosedAtStart(t *testing.T) {
	h := newHarness(t)
	h.cfg.Desktop.Relaunch = "always"
	h.closeAtStart()
	if err := h.run("yes"); err != nil {
		t.Fatal(err)
	}
	calls := h.app.Calls()
	if len(calls) != 1 || calls[0] != "launch "+tDesktop {
		t.Errorf("app calls = %v", calls)
	}
	if got := cardRow(h.output(), "Hermes app"); got != "" {
		t.Errorf("row shown although it was reopened: %q", got)
	}
	if got := cardRow(h.output(), "Time"); !strings.Contains(got, "until Hermes reopened") {
		t.Errorf("Time = %q", got)
	}
}

func TestRelaunchAlwaysRunsPostRelaunchHooks(t *testing.T) {
	h := newHarness(t)
	h.cfg.Desktop.Relaunch = "always"
	h.closeAtStart()
	var hooked int
	h.runner.OnFunc([]string{"hook-x"}, func(execx.Cmd) execx.Result { hooked++; return execx.Result{Output: "done"} })
	h.cfg.Hooks = []config.Hook{{Name: "notify", When: config.HookPostRelaunch, Run: []string{"hook-x"}}}
	if err := h.run("yes"); err != nil {
		t.Fatal(err)
	}
	if hooked != 1 {
		t.Errorf("post-relaunch hook ran %d times after a reopen", hooked)
	}
}

func TestRelaunchAlwaysOnlyAfterAVerifiedUpdate(t *testing.T) {
	h := newHarness(t)
	h.cfg.Desktop.Relaunch = "always"
	h.closeAtStart()
	h.verifier.ok = false
	err := h.run("yes")
	if !errors.Is(err, apperr.ErrUpdateProblem) {
		t.Fatalf("err = %v", err)
	}
	if launched(h) {
		t.Errorf("reopened after an unverified update: %v", h.app.Calls())
	}
	want := "not reopened (it was closed when the update started, and the update is not verified)"
	if got := cardRow(h.output(), "Hermes app"); got != want {
		t.Errorf("Hermes app = %q", got)
	}
}

func TestRelaunchAlwaysNotAfterAFailedUpdateEvenInTheFinallyPath(t *testing.T) {
	h := newHarness(t)
	h.cfg.Desktop.Relaunch = "always"
	h.closeAtStart()
	h.onUpdate(1, "→ Fetching updates\nerror: npm build failed", false)
	if err := h.run("yes"); err == nil {
		t.Fatal("want the problem exit")
	}
	if launched(h) {
		t.Errorf("reopened after a failed update: %v", h.app.Calls())
	}
}

func TestRelaunchAlwaysNeverInAnUnattendedRunForAClosedApp(t *testing.T) {
	h := newHarness(t)
	h.cfg.Desktop.Relaunch = "always"
	h.closeAtStart()
	if err := h.run("unattended"); err != nil {
		t.Fatal(err)
	}
	if launched(h) {
		t.Errorf("unattended run opened a window nobody asked for: %v", h.app.Calls())
	}
	want := "not reopened (it was closed when the update started, and unattended runs only reopen an app they closed)"
	if got := cardRow(h.output(), "Hermes app"); got != want {
		t.Errorf("Hermes app = %q", got)
	}
}

func TestRelaunchUnattendedKeepsGivingBackWhatItClosed(t *testing.T) {
	for _, pol := range []string{"auto", "always"} {
		h := newHarness(t)
		h.cfg.Desktop.Relaunch = pol
		if err := h.run("unattended"); err != nil {
			t.Fatal(err)
		}
		if !launched(h) {
			t.Errorf("%s: unattended run did not reopen the app it closed: %v", pol, h.app.Calls())
		}
	}
	// auto + closed + unattended: not reopened, and the hint must not promise
	// that "always" would change that
	h := newHarness(t)
	h.closeAtStart()
	if err := h.run("unattended"); err != nil {
		t.Fatal(err)
	}
	got := cardRow(h.output(), "Hermes app")
	if launched(h) || strings.Contains(got, "set desktop.relaunch") || !strings.Contains(got, "unattended") {
		t.Errorf("launched=%v row=%q", launched(h), got)
	}
}

func TestRelaunchNeverLeavesAnOpenAppClosedAndSaysWhy(t *testing.T) {
	h := newHarness(t)
	h.cfg.Desktop.Relaunch = "never"
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if launched(h) {
		t.Errorf("reopened with desktop.relaunch: never: %v", h.app.Calls())
	}
	want := `not reopened (desktop.relaunch is "never")`
	if got := cardRow(h.output(), "Hermes app"); got != want {
		t.Errorf("Hermes app = %q, want %q", got, want)
	}
	// also after a failed update and in an unattended run
	h2 := newHarness(t)
	h2.cfg.Desktop.Relaunch = "never"
	h2.onUpdate(1, "→ Fetching updates\nerror", false)
	_ = h2.run("unattended")
	if launched(h2) {
		t.Errorf("never, failed update: %v", h2.app.Calls())
	}
	// closed at the start and "never": nothing to explain
	h3 := newHarness(t)
	h3.cfg.Desktop.Relaunch = "never"
	h3.closeAtStart()
	if err := h3.run("yes"); err != nil {
		t.Fatal(err)
	}
	if launched(h3) || cardRow(h3.output(), "Hermes app") != "" {
		t.Errorf("launched=%v row=%q", launched(h3), cardRow(h3.output(), "Hermes app"))
	}
}

func TestRelaunchNoRelaunchFlagBeatsAlwaysAndSaysWhy(t *testing.T) {
	h := newHarness(t)
	h.cfg.Desktop.Relaunch = "always"
	h.closeAtStart()
	if err := h.run("yes", "no-relaunch"); err != nil {
		t.Fatal(err)
	}
	if launched(h) {
		t.Errorf("--no-relaunch ignored: %v", h.app.Calls())
	}
	if got := cardRow(h.output(), "Hermes app"); got != "not reopened (--no-relaunch was given)" {
		t.Errorf("Hermes app = %q", got)
	}
}

func TestRelaunchWithoutADesktopInstallStaysQuiet(t *testing.T) {
	for _, pol := range []string{"auto", "always", "never"} {
		h := newHarness(t)
		h.cfg.Desktop.Relaunch = pol
		h.install.Desktop = ""
		h.closeAtStart()
		if err := h.run("yes"); err != nil {
			t.Fatal(err)
		}
		if launched(h) || cardRow(h.output(), "Hermes app") != "" || strings.Contains(h.output(), "desktop exe missing") {
			t.Errorf("%s, headless install: calls=%v\n%s", pol, h.app.Calls(), h.output())
		}
	}
}

func TestRelaunchFailureIsExplainedAndOnlyAWarning(t *testing.T) {
	h := newHarness(t)
	h.cfg.Desktop.Relaunch = "always"
	h.closeAtStart()
	h.exists = func(p string) bool { return p != tDesktop }
	if err := h.run("yes"); err != nil {
		t.Fatalf("a missing exe must not fail a verified update: %v", err)
	}
	want := "not reopened (the desktop app file is missing; run: hermes desktop --force-build)"
	if got := cardRow(h.output(), "Hermes app"); got != want {
		t.Errorf("Hermes app = %q", got)
	}
	if cardRow(h.output(), "Warnings") == "" {
		t.Error("the missing exe is not counted as a warning")
	}
}

func TestRelaunchAlwaysDoesNotReopenWhenTheRunWasCancelled(t *testing.T) {
	h := newHarness(t)
	h.cfg.Desktop.Relaunch = "always"
	h.closeAtStart()
	h.sessions.script = [][]hermes.Session{busy("s1")}
	h.cfg.Run.Unattended = false
	h.con.Keys = []platform.Key{key('a')}
	err := h.run()
	if !errors.Is(err, apperr.ErrCancelled) {
		t.Fatalf("err = %v", err)
	}
	if launched(h) {
		t.Errorf("a cancelled run (nothing closed) opened the app: %v", h.app.Calls())
	}
}
