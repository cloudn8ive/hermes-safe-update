package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
	"github.com/cloudn8ive/hermes-safe-update/internal/ui"
)

// Truthful verify (v0.4.0): the tolerated relaunch exit (#123971), the stale
// gateway, the gateway rescue (#129171), the double relaunch row (#126821)
// and the launcher after the update.

// hermesFixture reads a synthetic Hermes output from the hermes package.
func hermesFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "hermes", "testdata", "contracts", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// cardRow returns the value of the row with that key in the last card.
func summaryRow(cards []ui.Card, key string) (ui.Row, bool) {
	if len(cards) == 0 {
		return ui.Row{}, false
	}
	for _, r := range cards[len(cards)-1].Rows {
		if r.Key == key {
			return r, true
		}
	}
	return ui.Row{}, false
}

func (h *harness) withCards() *[]ui.Card {
	cards := []ui.Card{}
	h.cards = &cards
	return &cards
}

// gatewayComesBackOnNewCode makes `gateway start` bring up a gateway on the
// new code.
func (h *harness) gatewayComesBackOnNewCode() {
	h.gw.onStart = func() {
		h.procs.Add(gatewayProc())
		h.gw.mu.Lock()
		h.gw.state.CodeSHA = newSHA
		h.gw.mu.Unlock()
	}
}

// ---- #123971 E1: the relaunch-unverified exit 1 ----

func TestRelaunchUnverifiedExit1WithHealthyGatewayIsOK(t *testing.T) {
	h := newHarness(t)
	cards := h.withCards()
	h.onUpdate(1, hermesFixture(t, "update-relaunch-unverified.txt"), true) // HEAD and the gateway on the new code
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	h.mustContain("RESULT: OK (updater exited 1 on its own unverified gateway relaunch, hermes-agent #123971; gateway verified on the new code by Safe Update)")
	row, ok := summaryRow(*cards, "Gateway check")
	if !ok || row.Verdict == nil || *row.Verdict != ui.VerdictAttention || !strings.Contains(row.Value, "could not be verified for: default") {
		t.Errorf("row = %+v, %v (an amber note naming the Hermes line that was tolerated)", row, ok)
	}
	if h.gw.stops != 0 || h.gw.starts != 0 {
		t.Errorf("a healthy gateway was touched: stops=%d starts=%d", h.gw.stops, h.gw.starts)
	}
}

func TestRelaunchUnverifiedExit1StaysAProblemWhenAnythingElseFailed(t *testing.T) {
	cases := map[string]struct {
		out     string
		advance bool
	}{
		"another failure line":        {hermesFixture(t, "update-relaunch-unverified-other-failure.txt"), true},
		"gateway not on the new code": {hermesFixture(t, "update-relaunch-unverified.txt"), false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			cards := h.withCards()
			h.onUpdate(1, c.out, c.advance)
			if !c.advance {
				h.update = func(int) execx.Result { h.repo.set(newSHA); return execx.Result{Code: 1, Output: c.out} }
				h.gw.onStart = nil // and nothing brings the gateway to the new code
				h.gw.stopErr = nil
			}
			h.con.Keys = []platform.Key{key('y')}
			err := h.run()
			if !errors.Is(err, apperr.ErrUpdateProblem) {
				t.Fatalf("err = %v\n%s", err, h.output())
			}
			h.mustContain("RESULT: PROBLEM")
			if _, ok := summaryRow(*cards, "Gateway check"); ok {
				t.Error("a tolerance row for an exit that was not tolerated")
			}
		})
	}
}

// ---- #123971 E2: alive, but still on the old code ----

func TestAliveGatewayOnOldCodeIsRestartedOnce(t *testing.T) {
	h := newHarness(t)
	cards := h.withCards()
	h.update = func(int) execx.Result { // HEAD moves, the gateway keeps running the old code
		h.repo.set(newSHA)
		return execx.Result{Output: "→ Fetching updates\nUpdate complete! (v1.0 → v1.1)"}
	}
	h.gw.onStop = func() { h.procs.Remove(200) }
	h.gatewayComesBackOnNewCode()
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if h.gw.stops != 1 || h.gw.starts != 1 {
		t.Errorf("stops=%d starts=%d, want one restart", h.gw.stops, h.gw.starts)
	}
	h.mustContain("RESULT: OK")
	row, ok := summaryRow(*cards, "Stale gateway")
	if !ok || !strings.Contains(row.Value, "old code") || !strings.Contains(row.Value, "now runs the new code") || row.Verdict == nil {
		t.Errorf("row = %+v, %v", row, ok)
	}
}

// The E2 restart stops this gateway only (never `gateway stop --all`, which
// takes down every profile's gateway on macOS/Linux); the close stage keeps
// using Stop.
func TestStaleGatewayRestartUsesTheSingleGatewayStop(t *testing.T) {
	h := newHarness(t)
	h.update = func(int) execx.Result {
		h.repo.set(newSHA)
		return execx.Result{Output: "Update complete! (v1.0 → v1.1)"}
	}
	h.gw.onStop = func() { h.procs.Remove(200) }
	h.gatewayComesBackOnNewCode()
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if h.gw.staleStops != 1 {
		t.Errorf("stale stops = %d, want 1", h.gw.staleStops)
	}
	if h.gw.fullStops != 0 {
		t.Errorf("Stop (which may run `gateway stop --all`) was called %d times in the restart path", h.gw.fullStops)
	}
}

// With another profile's gateway running, the generic Start returns at once
// ("a gateway exists") and the stopped gateway would never come back. The
// restart must use StartAfterStale (the CLIGateway tests pin that it runs
// `gateway start` whatever else is alive), not Start.
func TestStaleGatewayRestartStartsThroughStartAfterStale(t *testing.T) {
	h := newHarness(t)
	h.update = func(int) execx.Result {
		h.repo.set(newSHA)
		return execx.Result{Output: "Update complete! (v1.0 → v1.1)"}
	}
	h.gw.onStop = func() { h.procs.Remove(200) }
	h.gatewayComesBackOnNewCode()
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if h.gw.staleStarts != 1 {
		t.Errorf("StartAfterStale calls = %d, want 1 (the generic Start is a no-op when another gateway runs)", h.gw.staleStarts)
	}
}

// The start after the stale stop failed (or started nothing): the log and
// the summary say the gateway was stopped and is not back, the run is a
// PROBLEM, and no "ok" is logged.
func TestStaleGatewayThatCouldNotBeStartedAgainIsReported(t *testing.T) {
	h := newHarness(t)
	cards := h.withCards()
	h.update = func(int) execx.Result {
		h.repo.set(newSHA)
		return execx.Result{Output: "Update complete! (v1.0 → v1.1)"}
	}
	h.gw.onStop = func() { h.procs.Remove(200) }
	h.gw.staleStartErr = errors.New("`gateway start` ran but no new gateway process appeared")
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); !errors.Is(err, apperr.ErrUpdateProblem) {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if h.gw.starts != 0 {
		t.Errorf("starts = %d; nothing was started", h.gw.starts)
	}
	row, ok := summaryRow(*cards, "Stale gateway")
	if !ok || !strings.Contains(row.Value, "stopped it but could not start it again") || !strings.Contains(row.Value, "no new gateway process appeared") || strings.Contains(row.Value, "restarted it") {
		t.Errorf("row = %+v, %v", row, ok)
	}
	h.mustContain("RESULT: PROBLEM", "could not be started again")
	if strings.Contains(h.output(), "gateway start (it was on the old code): ok") {
		t.Errorf("the log says the start worked:\n%s", h.output())
	}
}

// StopStale returns nil without stopping anything when the state pid is not a
// process Safe Update classes as the gateway. The row must not say Safe Update
// stopped it, and no start may be issued on top of a gateway that still runs.
func TestStaleGatewayThatWasNotStoppedIsNotReportedAsStopped(t *testing.T) {
	h := newHarness(t)
	cards := h.withCards()
	h.update = func(int) execx.Result {
		h.repo.set(newSHA)
		return execx.Result{Output: "Update complete! (v1.0 → v1.1)"}
	}
	// onStop does not remove the process: StopStale "succeeds" and pid 200 lives on.
	h.con.Keys = []platform.Key{key('y')}
	_ = h.run()
	if h.gw.staleStops != 1 {
		t.Fatalf("stale stops = %d: the test does not reach the restart\n%s", h.gw.staleStops, h.output())
	}
	if h.gw.staleStarts != 0 || h.gw.starts != 0 {
		t.Errorf("staleStarts=%d starts=%d, want no start while the gateway still runs", h.gw.staleStarts, h.gw.starts)
	}
	row, ok := summaryRow(*cards, "Stale gateway")
	if !ok || !strings.Contains(row.Value, "not restarted:") || !strings.Contains(row.Value, "could not be stopped") || strings.Contains(row.Value, "Safe Update stopped it") {
		t.Errorf("row = %+v, %v", row, ok)
	}
	h.mustContain("RESULT: PROBLEM")
}

// The start answered no, but the gateway is up on the new code (its process
// was not seen as new, or a supervisor brought it back): the run verifies OK
// and the row agrees, instead of "could not start it again" next to RESULT OK.
func TestStaleGatewayThatIsUpOnTheNewCodeDespiteAStartErrorIsReportedAsRestarted(t *testing.T) {
	h := newHarness(t)
	cards := h.withCards()
	h.update = func(int) execx.Result {
		h.repo.set(newSHA)
		return execx.Result{Output: "Update complete! (v1.0 → v1.1)"}
	}
	h.gw.onStop = func() { h.procs.Remove(200) }
	h.gw.staleStartErr = errors.New("`gateway start` ran but no new gateway process appeared")
	h.gw.onStaleStart = func() { // the gateway comes up anyway, on the new code
		h.procs.Add(gatewayProc())
		h.gw.mu.Lock()
		h.gw.state.CodeSHA = newSHA
		h.gw.mu.Unlock()
	}
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	h.mustContain("RESULT: OK")
	row, ok := summaryRow(*cards, "Stale gateway")
	if !ok || !strings.Contains(row.Value, "now runs the new code") || strings.Contains(row.Value, "could not start") {
		t.Errorf("row = %+v, %v", row, ok)
	}
	if strings.Contains(h.output(), "could not be started again") {
		t.Errorf("a warning claims the start failed:\n%s", h.output())
	}
}

func TestStaleGatewayThatStaysStaleIsRestartedOnlyOnce(t *testing.T) {
	h := newHarness(t)
	cards := h.withCards()
	h.update = func(int) execx.Result {
		h.repo.set(newSHA)
		return execx.Result{Output: "Update complete! (v1.0 → v1.1)"}
	}
	h.gw.onStop = func() { h.procs.Remove(200) }
	h.gw.onStart = func() { h.procs.Add(gatewayProc()) } // comes back, still reporting the old code
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); !errors.Is(err, apperr.ErrUpdateProblem) {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if h.gw.stops != 1 || h.gw.starts != 1 {
		t.Errorf("stops=%d starts=%d, want exactly one restart", h.gw.stops, h.gw.starts)
	}
	if row, _ := summaryRow(*cards, "Stale gateway"); !strings.Contains(row.Value, "did not report the new code") {
		t.Errorf("row = %+v", row)
	}
}

func TestStaleGatewayIsLeftAloneAfterAnUpdateThatStoppedPartway(t *testing.T) {
	h := newHarness(t)
	cards := h.withCards()
	h.update = func(int) execx.Result { // code moved, no "Update complete!", exit 1
		h.repo.set(newSHA)
		return execx.Result{Code: 1, Output: "→ Pulling updates...\n✗ build failed"}
	}
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); !errors.Is(err, apperr.ErrUpdateProblem) {
		t.Fatalf("err = %v", err)
	}
	if h.gw.stops != 0 || h.gw.starts != 0 {
		t.Errorf("a torn tree: stops=%d starts=%d", h.gw.stops, h.gw.starts)
	}
	if row, ok := summaryRow(*cards, "Stale gateway"); !ok || !strings.Contains(row.Value, "not restarted") {
		t.Errorf("row = %+v, %v", row, ok)
	}
}

// ---- #129171: the gateway rescue ----

func hermesPausesGateway(h *harness) {
	h.procs.Remove(200) // Hermes stopped the gateway for the update
}

func TestAbortedUpdateRestartsGatewayHermesPaused(t *testing.T) {
	h := newHarness(t)
	cards := h.withCards()
	ctx, cancel := context.WithCancel(context.Background())
	h.ctx = ctx
	h.update = func(int) execx.Result {
		hermesPausesGateway(h)
		cancel() // Ctrl+C: the verify stage never runs
		return execx.Result{Code: 1, Output: "→ Fetching updates"}
	}
	h.con.Keys = []platform.Key{key('y')}
	err := h.run()
	if apperr.ExitCode(err) != apperr.Failure {
		t.Fatalf("exit %d (%v): the abort is still a failure", apperr.ExitCode(err), err)
	}
	if h.gw.starts != 1 {
		t.Fatalf("starts = %d: the gateway Hermes paused stayed down\n%s", h.gw.starts, h.output())
	}
	row, ok := summaryRow(*cards, "Gateway rescue")
	if !ok || !strings.Contains(row.Value, "restarted by Safe Update") || row.Verdict == nil {
		t.Errorf("row = %+v, %v", row, ok)
	}
}

func TestHermesHeldGatewayIsNotStartedBySafeUpdate(t *testing.T) {
	held := hermesFixture(t, "update-gateway-held.txt")
	for _, abort := range []bool{false, true} {
		name := "verify stage"
		if abort {
			name = "abort path"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			cards := h.withCards()
			ctx, cancel := context.WithCancel(context.Background())
			h.ctx = ctx
			h.update = func(int) execx.Result {
				hermesPausesGateway(h)
				if abort {
					cancel()
				}
				return execx.Result{Code: 1, Output: held}
			}
			h.con.Keys = []platform.Key{key('y')}
			if err := h.run(); !errors.Is(err, apperr.ErrUpdateProblem) {
				t.Fatalf("err = %v", err)
			}
			if h.gw.starts != 0 {
				t.Errorf("starts = %d: Hermes left the gateway stopped on purpose", h.gw.starts)
			}
			row, ok := summaryRow(*cards, "Gateway rescue")
			if !ok || !strings.Contains(row.Value, "not started") || !strings.Contains(row.Value, "the checkout is not whole") {
				t.Errorf("row = %+v, %v", row, ok)
			}
		})
	}
}

func TestTornTreeAbortDoesNotStartGateway(t *testing.T) {
	for _, moved := range []bool{true, false} {
		name := "head unchanged"
		if moved {
			name = "head moved"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			ctx, cancel := context.WithCancel(context.Background())
			h.ctx = ctx
			h.update = func(int) execx.Result {
				hermesPausesGateway(h)
				if moved {
					h.repo.set(newSHA)
				}
				cancel()
				return execx.Result{Code: 1, Output: "→ Pulling updates..."}
			}
			h.con.Keys = []platform.Key{key('y')}
			_ = h.run()
			want := 1 // before the code moved the old tree is whole: rescue it
			if moved {
				want = 0
			}
			if h.gw.starts != want {
				t.Errorf("starts = %d, want %d", h.gw.starts, want)
			}
		})
	}
}

// The verify stage reaches the gateway start after an update that was killed
// or failed partway. It refuses on a torn tree like the abort path and the
// stale restart do, and starts on a whole one.
func TestVerifyStageDoesNotStartGatewayAfterATornUpdate(t *testing.T) {
	for _, tc := range []struct {
		name      string
		moved     bool
		out       string
		code      int
		wantStart int
	}{
		{"failed after HEAD moved", true, "→ Pulling updates...\n✗ build failed", 1, 0},
		{"failed before HEAD moved", false, "→ Pulling updates...\n✗ fetch failed", 1, 1},
		{"clean exit", true, "Update complete! (v1.0 → v1.1)", 0, 1},
		{"killed after the update completed (rc 1, Update complete!, HEAD moved)", true, "→ Pulling updates...\nUpdate complete! (v1.0 → v1.1)\n✗ timed out in a post-update step", 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			cards := h.withCards()
			h.update = func(int) execx.Result {
				hermesPausesGateway(h)
				if tc.moved {
					h.repo.set(newSHA)
				}
				return execx.Result{Code: tc.code, Output: tc.out}
			}
			h.gatewayComesBackOnNewCode()
			h.con.Keys = []platform.Key{key('y')}
			_ = h.run()
			if h.gw.starts != tc.wantStart {
				t.Fatalf("starts = %d, want %d\n%s", h.gw.starts, tc.wantStart, h.output())
			}
			row, ok := summaryRow(*cards, "Gateway rescue")
			if tc.wantStart == 0 {
				if !ok || !strings.Contains(row.Value, "not started: the update stopped partway after the code changed") {
					t.Errorf("row = %+v, %v", row, ok)
				}
			} else if ok && strings.Contains(row.Value, "not started") {
				t.Errorf("row = %+v", row)
			}
		})
	}
}

// A gateway Safe Update stopped itself (the pause gate could not be mapped)
// is started in the verify stage's first step. That start is not exempt from
// the guard: after an update that stopped partway the tree may be torn, so
// nothing is started, and the summary says so once.
func TestVerifyStageDoesNotStartTheGatewayItStoppedAfterATornUpdate(t *testing.T) {
	for _, tc := range []struct {
		name      string
		moved     bool
		out       string
		wantStart int
	}{
		{"failed after HEAD moved", true, "→ Pulling updates...\n✗ build failed", 0},
		{"failed before HEAD moved", false, "→ Pulling updates...\n✗ fetch failed", 1},
		{"update completed", true, "Update complete! (v1.0 → v1.1)", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			cards := h.withCards()
			h.gw.probes = []hermes.GateResult{hermes.GateBlocked, hermes.GateOK} // pre-flight: the gateway is stopped first
			h.gw.onStop = func() { h.procs.Remove(200) }
			code := 0
			if !strings.Contains(tc.out, "Update complete!") {
				code = 1
			}
			h.update = func(int) execx.Result {
				if tc.moved {
					h.repo.set(newSHA)
				}
				return execx.Result{Code: code, Output: tc.out}
			}
			if tc.moved {
				h.gatewayComesBackOnNewCode()
			} else {
				h.gw.onStart = func() { h.procs.Add(gatewayProc()) } // still the old code, which is the code on disk
			}
			h.con.Keys = []platform.Key{key('y')}
			_ = h.run()
			if h.gw.stops != 1 {
				t.Fatalf("stops = %d: the gateway was not stopped by Safe Update, the test does not exercise the early start\n%s", h.gw.stops, h.output())
			}
			if h.gw.starts != tc.wantStart {
				t.Fatalf("starts = %d, want %d\n%s", h.gw.starts, tc.wantStart, h.output())
			}
			if tc.wantStart == 0 {
				row, ok := summaryRow(*cards, "Gateway rescue")
				if !ok || !strings.Contains(row.Value, "not started: the update stopped partway after the code changed") {
					t.Errorf("row = %+v, %v", row, ok)
				}
				if n := strings.Count(h.output(), "was not started"); n != 1 {
					t.Errorf("the refusal is logged %d times, want once\n%s", n, h.output())
				}
			}
		})
	}
}

// A gateway that was deliberately not started cannot come up by waiting: the
// verify stage must not sit through verify_wait_s for it.
func TestRefusedGatewayStartSkipsTheVerifyWait(t *testing.T) {
	h := newHarness(t)
	h.cfg.Tunables.VerifyWaitS = 1000 // far above anything else the run sleeps
	h.gw.probes = []hermes.GateResult{hermes.GateBlocked, hermes.GateOK}
	h.gw.onStop = func() { h.procs.Remove(200) }
	h.update = func(int) execx.Result { // torn: code moved, no "Update complete!", exit 1
		h.repo.set(newSHA)
		return execx.Result{Code: 1, Output: "→ Pulling updates...\n✗ build failed"}
	}
	h.con.Keys = []platform.Key{key('y')}
	_ = h.run()
	if h.gw.stops != 1 || h.gw.starts != 0 {
		t.Fatalf("stops=%d starts=%d: the test does not reach the refused early start\n%s", h.gw.stops, h.gw.starts, h.output())
	}
	if h.slept >= 1000*time.Second {
		t.Errorf("slept %v: the verify stage waited for a gateway it did not start", h.slept)
	}
}

func TestRescueRowAndVerifiedUnchanged(t *testing.T) {
	h := newHarness(t)
	cards := h.withCards()
	h.update = func(int) execx.Result {
		h.advance()
		hermesPausesGateway(h) // the update ends with the gateway down
		return execx.Result{Output: "→ Stopping Windows gateway (pid 200)…\nUpdate complete! (v1.0 → v1.1)"}
	}
	h.gatewayComesBackOnNewCode()
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	h.mustContain("RESULT: OK", "Hermes is updated and verified.")
	row, ok := summaryRow(*cards, "Gateway rescue")
	if !ok || !strings.Contains(row.Value, "restarted by Safe Update") || !strings.Contains(row.Value, "down ") {
		t.Errorf("row = %+v, %v (Safe Update brought the gateway back; the summary must say so)", row, ok)
	}
	if c := (*cards)[len(*cards)-1]; c.OK == nil || !*c.OK {
		t.Errorf("the rescue row flipped the verdict: %+v", c.OK)
	}
}

func TestNoRescueRowWhenHermesBroughtTheGatewayBack(t *testing.T) {
	h := newHarness(t)
	cards := h.withCards()
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v", err)
	}
	for _, k := range []string{"Gateway rescue", "Stale gateway", "Gateway check", "Double launch", "Launcher"} {
		if _, ok := summaryRow(*cards, k); ok {
			t.Errorf("row %q on a clean run", k)
		}
	}
}

// ---- #126821: two relaunches ----

func TestSummaryRowDoubleRelaunch(t *testing.T) {
	h := newHarness(t)
	cards := h.withCards()
	h.onUpdate(0, hermesFixture(t, "update-double-relaunch.txt"), true)
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v", err)
	}
	row, ok := summaryRow(*cards, "Double launch")
	if !ok || !strings.Contains(row.Value, "profile work twice") || !strings.Contains(row.Value, "#126821") || row.Verdict == nil || *row.Verdict != ui.VerdictAttention {
		t.Errorf("row = %+v, %v", row, ok)
	}
	if c := (*cards)[len(*cards)-1]; c.OK == nil || !*c.OK {
		t.Error("the note must not change the verdict")
	}
}

func TestNoRowWhenProfilesDiffer(t *testing.T) {
	h := newHarness(t)
	cards := h.withCards()
	h.onUpdate(0, hermesFixture(t, "update-relaunch-different-profiles.txt"), true)
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v", err)
	}
	if _, ok := summaryRow(*cards, "Double launch"); ok {
		t.Error("a row for two different profiles")
	}
}

// ---- the launcher after the update ----

const tLauncher2 = tHome + `\hermes-agent\.hermes\bin\hermes.exe`

// resolvingLocator also answers ResolveLauncher, like the real locator.
type resolvingLocator struct {
	fakeLocator
	launcher string
	prefix   []string
	err      error
	calls    int
}

func (l *resolvingLocator) ResolveLauncher(context.Context, hermes.Install) (string, []string, error) {
	l.calls++
	return l.launcher, l.prefix, l.err
}

// launcherSetup runs the harness with a route and a resolver; the update
// removes tLauncher.
func launcherSetup(t *testing.T, loc *resolvingLocator) (*harness, *hermes.LauncherRoute, *[]*Run) {
	t.Helper()
	h := newHarness(t)
	route := &hermes.LauncherRoute{}
	var runs []*Run
	removed := false
	h.exists = func(p string) bool { return !(removed && p == tLauncher) }
	h.onUpdate(0, "→ Fetching updates\nUpdate complete! (v1.0 → v1.1)", true)
	inner := h.update
	h.update = func(n int) execx.Result { removed = true; return inner(n) }
	loc.fakeLocator = fakeLocator{in: h.install}
	h.onRun = func(r *Run) {
		r.Locator, r.Route, r.Runner = loc, route, route.Wrap(r.Runner)
		runs = append(runs, r)
	}
	h.con.Keys = []platform.Key{key('y')}
	return h, route, &runs
}

func argvs(h *harness) [][]string {
	var out [][]string
	for _, c := range h.runner.Calls() {
		out = append(out, c.Argv)
	}
	return out
}

func ranWith(h *harness, first string, rest ...string) bool {
	for _, a := range argvs(h) {
		if len(a) >= 1+len(rest) && a[0] == first && strings.Join(a[1:1+len(rest)], "\x00") == strings.Join(rest, "\x00") {
			return true
		}
	}
	return false
}

func TestLauncherReplacedByTheUpdateIsFollowed(t *testing.T) {
	loc := &resolvingLocator{launcher: tLauncher2}
	h, route, runs := launcherSetup(t, loc)
	cards := h.withCards()
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if loc.calls != 1 {
		t.Errorf("resolved %d times", loc.calls)
	}
	if got := (*runs)[0].Facts.Install.Launcher; got != tLauncher2 {
		t.Errorf("launcher = %q", got)
	}
	// everything built with the old path (gateway, verify, cua) now reaches the new launcher
	if _, err := route.Wrap(h.runner).Run(context.Background(), execx.Cmd{Argv: []string{tLauncher, "gateway", "start", "--all"}}); err != nil {
		t.Fatal(err)
	}
	if !ranWith(h, tLauncher2, "gateway", "start", "--all") {
		t.Errorf("the old path still ran: %v", argvs(h))
	}
	if !ranWith(h, tLauncher2, "--version") {
		t.Errorf("the version read after the update did not use the new launcher: %v", argvs(h))
	}
	row, ok := summaryRow(*cards, "Launcher")
	if !ok || !strings.Contains(row.Value, "is gone after the update") || !strings.Contains(row.Value, tLauncher2) {
		t.Errorf("row = %+v, %v", row, ok)
	}
	if c := (*cards)[len(*cards)-1]; c.OK == nil || !*c.OK {
		t.Error("the launcher row flipped the verdict")
	}
}

func TestLauncherGoneRunsHermesThroughItsPython(t *testing.T) {
	prefix := []string{"py", "-I", "-c", "boot"}
	loc := &resolvingLocator{launcher: tLauncher, prefix: prefix}
	h, route, runs := launcherSetup(t, loc)
	cards := h.withCards()
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	in := (*runs)[0].Facts.Install
	if len(in.LauncherPrefix) != 4 {
		t.Errorf("install = %+v", in)
	}
	if _, err := route.Wrap(h.runner).Run(context.Background(), execx.Cmd{Argv: []string{tLauncher, "gateway", "start", "--all"}}); err != nil {
		t.Fatal(err)
	}
	if !ranWith(h, "py", "-I", "-c", "boot", "gateway", "start", "--all") {
		t.Errorf("launcher-free route not used: %v", argvs(h))
	}
	row, ok := summaryRow(*cards, "Launcher")
	if !ok || !strings.Contains(row.Value, "managed Python") {
		t.Errorf("row = %+v, %v", row, ok)
	}
}

// The resolver's first candidate (where the launcher-free command is anchored)
// is not the path the run was wired with. Everything that runs the install's
// launcher after the update (the version read, a retry, hooks) must still go
// through the prefix.
func TestLauncherFreeRouteAlsoCoversTheInstallLauncherWhenTheResolverNamesAnotherPath(t *testing.T) {
	prefix := []string{"py", "-I", "-c", "boot"}
	loc := &resolvingLocator{launcher: tLauncher2, prefix: prefix}
	h, _, runs := launcherSetup(t, loc)
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	in := (*runs)[0].Facts.Install
	if in.Launcher != tLauncher || len(in.LauncherPrefix) != 4 {
		t.Errorf("install launcher = %q prefix = %v, want the wired launcher and the prefix", in.Launcher, in.LauncherPrefix)
	}
	if !ranWith(h, "py", "-I", "-c", "boot", "--version") {
		t.Errorf("the version read after the update did not go through the prefix: %v", argvs(h))
	}
	if ranWith(h, tLauncher2, "--version") {
		t.Errorf("the missing candidate was run directly: %v", argvs(h))
	}
}

func TestLauncherLostIsAWarningRowNotAVerdict(t *testing.T) {
	loc := &resolvingLocator{err: errors.New("no launcher found")}
	h, _, _ := launcherSetup(t, loc)
	cards := h.withCards()
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	row, ok := summaryRow(*cards, "Launcher")
	if !ok || !strings.Contains(row.Value, "none works after the update") || row.Verdict == nil {
		t.Errorf("row = %+v, %v", row, ok)
	}
}

func TestLauncherThatStillExistsIsNotLookedUpAgain(t *testing.T) {
	loc := &resolvingLocator{launcher: tLauncher2}
	h, _, _ := launcherSetup(t, loc)
	h.exists = nil // everything exists, the launcher too
	if err := h.run(); err != nil {
		t.Fatalf("err = %v", err)
	}
	if loc.calls != 0 {
		t.Errorf("resolved %d times although the launcher is there (an extra `--version` run on every update)", loc.calls)
	}
}

func TestInstallWithoutLauncherRunsThroughItsPythonFromTheStart(t *testing.T) {
	h := newHarness(t)
	route := &hermes.LauncherRoute{}
	in := h.install
	in.LauncherPrefix = []string{"py", "-I", "-c", "boot"}
	h.runner.OnFunc([]string{"py", "-I", "-c", "boot"}, func(c execx.Cmd) execx.Result {
		args := c.Argv[4:]
		switch {
		case len(args) > 0 && args[0] == "update" && strings.Contains(strings.Join(args, " "), "--check"):
			return execx.Result{Output: h.mainCheck}
		case len(args) > 0 && args[0] == "update" && len(args) > 1 && args[1] == "--yes":
			h.updates++
			return h.update(h.updates)
		}
		return execx.Result{}
	})
	h.onRun = func(r *Run) {
		r.Locator, r.Route, r.Runner = &resolvingLocator{fakeLocator: fakeLocator{in: in}}, route, route.Wrap(r.Runner)
	}
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if h.updates != 1 || !ranWith(h, "py", "-I", "-c", "boot", "update", "--yes") {
		t.Errorf("the update did not run through the managed Python: %v", argvs(h))
	}
	for _, a := range argvs(h) {
		if a[0] == tLauncher {
			t.Errorf("the missing launcher was run: %v", a)
		}
	}
	h.mustContain("Hermes runs through its managed Python")
}
