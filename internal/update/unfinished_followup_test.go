package update

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// The second review of v0.5.0 (R2-S2, N1, N2, N3, N7): synthetic logs and
// receipts only, fake processes and consoles.

// ---- R2-S2: a newer finished update clears the own-log signal ----

// Safe Update run X died inside `hermes update`; the user then ran
// `hermes update` by hand and it succeeded. X's log run still has no done
// line, but the work is finished.
func TestNewerFinishedUpdateClearsTheOwnLogSignal(t *testing.T) {
	xStart := crashedAt.Add(-3 * time.Minute)
	crashed := crashedRun(xStart, "→ Packaging the desktop app…")
	ranAt := xStart.Add(2 * time.Second) // its "== Running: hermes update" line
	mk := func(finished time.Time) *harness {
		h := finishHarness(t)
		h.receipts = &fakeReceipts{finished: finished} // the receipts name no unfinished update
		h.logTail = func(p string, _ int64) ([]byte, error) {
			if strings.HasSuffix(p, ".1") {
				return nil, os.ErrNotExist
			}
			return []byte(crashed + ownStart(t0.Add(40*time.Hour))), nil
		}
		return h
	}
	// the finished update began after X got to hermes update: nothing is owed
	h := mk(ranAt.Add(30 * time.Minute))
	var run *Run
	h.onRun = func(r *Run) { run = r }
	var buf strings.Builder
	h.logSink = &buf
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	h.mustContain("Hermes is up to date")
	if run.Facts.Previous != nil || h.updates != 0 || len(h.app.Calls()) != 0 || strings.Contains(h.output(), "last update") {
		t.Errorf("a finished update did not clear the own-log signal: previous=%v updates=%d app=%v\n%s", run.Facts.Previous, h.updates, h.app.Calls(), h.output())
	}
	if !strings.Contains(buf.String(), "an update finished after that run of Safe Update got to hermes update; not counted") {
		t.Errorf("the drop is not logged:\n%s", buf.String())
	}
	// one that began BEFORE X got there does not
	h = mk(ranAt.Add(-time.Hour))
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil || h.updates != 1 {
		t.Errorf("an older finished update hid the unfinished one: err=%v updates=%d\n%s", err, h.updates, h.output())
	}
	h.mustContain("did not finish (stopped during packaging the desktop app")
	// and none at all (or receipts that cannot be read) leaves it unfinished
	for name, f := range map[string]*fakeReceipts{
		"no finished update":  {},
		"receipts unreadable": {finishedErr: errors.New("denied")},
	} {
		h = mk(time.Time{})
		h.receipts = f
		h.con.Keys = []platform.Key{key('y')}
		if err := h.run(); err != nil || h.updates != 1 {
			t.Errorf("%s: err=%v updates=%d\n%s", name, err, h.updates, h.output())
		}
	}
}

// R3-S2: Safe Update run X died inside `hermes update`; its receipt was
// reconciled to "interrupted". The user then ran `hermes update` by hand and
// it FAILED early (a receipt "failed", started after X got there). That is
// not finished work: the newest receipt no longer names the unfinished update,
// so the own-log signal must stay and the next run goes on to finish it.
func TestFailedHandRunUpdateDoesNotClearTheOwnLogSignal(t *testing.T) {
	xStart := crashedAt.Add(-3 * time.Minute)
	home := t.TempDir() // real receipt files under a temp Hermes home
	dir := filepath.Join(append([]string{home}, hermes.ReceiptDirParts...)...)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	put := func(name, outcome string, started time.Time) {
		body := `{"outcome":"` + outcome + `","started_at":"` + started.Format(time.RFC3339) + `"}`
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("update_20260930_205300_4711_0f1e2d3c.json", "interrupted", xStart)
	put("update_20260930_212000_7005_1f1e2d3c.json", "failed", xStart.Add(30*time.Minute))
	h := finishHarness(t)
	h.receipts = hermes.ReceiptReader{Home: home}
	h.logTail = func(p string, _ int64) ([]byte, error) {
		if strings.HasSuffix(p, ".1") {
			return nil, os.ErrNotExist
		}
		return []byte(crashedRun(xStart, "→ Packaging the desktop app…") + ownStart(t0.Add(40*time.Hour))), nil
	}
	var run *Run
	h.onRun = func(r *Run) { run = r }
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil || h.updates != 1 {
		t.Fatalf("err=%v updates=%d\n%s", err, h.updates, h.output())
	}
	if run.Facts.Previous == nil {
		t.Errorf("a failed update hid the stopped run\n%s", h.output())
	}
	h.mustContain("did not finish (stopped during packaging the desktop app")
}

// The stale-marker signal is not the own log's: a leftover marker still counts.
func TestNewerFinishedUpdateDoesNotClearAStaleMarker(t *testing.T) {
	h := finishHarness(t)
	h.receipts = &fakeReceipts{finished: crashedAt.Add(time.Hour)}
	h.marker.stale = true
	h.logTail = func(p string, _ int64) ([]byte, error) {
		if strings.HasSuffix(p, ".1") {
			return nil, os.ErrNotExist
		}
		return []byte(crashedRun(crashedAt.Add(-3*time.Minute)) + ownStart(t0.Add(40*time.Hour))), nil
	}
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil || h.updates != 1 {
		t.Fatalf("err=%v updates=%d\n%s", err, h.updates, h.output())
	}
	h.mustContain("did not finish (an update marker was left behind)")
}

// ---- R2-N1: a skipped retry is not "up to date" ----

// The own log cannot be read, so whether an earlier run already tried is not
// known: a run that asks nobody does not close Hermes; a person who is asked
// decides.
func TestUnreadableOwnLogStopsUnattendedRetriesButNotAPersonWhoIsAsked(t *testing.T) {
	mk := func() *harness {
		h := finishHarness(t)
		h.logTail = func(string, int64) ([]byte, error) { return nil, errors.New("the log is locked") }
		return h
	}
	for _, flag := range []string{"unattended", "yes"} {
		t.Run(flag, func(t *testing.T) {
			h := mk()
			cards := h.withCards()
			var buf strings.Builder
			h.logSink = &buf
			// exit 1: nothing failed in this run, but the work is still owed
			if err := h.run(flag); !errors.Is(err, apperr.ErrNotRetried) || apperr.ExitCode(err) != apperr.Failure {
				t.Fatalf("err = %v (exit %d), want ErrNotRetried, exit 1\n%s", err, apperr.ExitCode(err), h.output())
			}
			if h.updates != 0 || len(h.app.Calls()) != 0 {
				t.Errorf("closed Hermes without knowing whether it already tried: updates=%d app=%v", h.updates, h.app.Calls())
			}
			h.mustContain("could not be read (logging.level warn or error also hides the lines it looks for), so it cannot tell whether an earlier attempt already failed; not trying again without asking")
			if strings.Contains(h.output(), "Hermes is up to date") {
				t.Errorf("not 'up to date':\n%s", h.output())
			}
			if row, ok := summaryRow(*cards, "Not retried"); !ok || !strings.Contains(row.Value, "own log could not be read (logging.level") {
				t.Errorf("row = %+v, %v", row, ok)
			}
			if !strings.Contains(buf.String(), "the own log cannot say whether an earlier run already tried to finish it") {
				t.Errorf("the unknown once-rule is not logged:\n%s", buf.String())
			}
		})
	}
	h := mk() // a person who is asked decides
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil || h.updates != 1 {
		t.Errorf("interactive: err=%v updates=%d\n%s", err, h.updates, h.output())
	}
	h = mk() // --force goes on anyway
	if err := h.run("unattended", "force"); err != nil || h.updates != 1 {
		t.Errorf("--force: err=%v updates=%d", err, h.updates)
	}
}

// R3-S3: the skip is logged with WARN, which survives logging.level warn and
// error (an info line would be dropped there, and a scheduled task would keep
// no trace of why the owed work was not done), and exits 1.
func TestSkippedRetryIsLoggedAtWarnLevelAndExitsOne(t *testing.T) {
	causes := map[string]func() *harness{
		"own log unreadable": func() *harness {
			h := finishHarness(t)
			h.logTail = func(string, int64) ([]byte, error) { return nil, errors.New("the log is locked") }
			return h
		},
		"earlier attempt failed": func() *harness {
			h := finishHarness(t)
			h.receipts = &fakeReceipts{prev: receiptDiedWithKey()}
			h.logTail = func(p string, _ int64) ([]byte, error) {
				if strings.HasSuffix(p, ".1") {
					return nil, os.ErrNotExist
				}
				return []byte(triedRun(t0, pidOld, receiptKey) + ownStart(t0.Add(time.Hour))), nil
			}
			return h
		},
	}
	for name, mk := range causes {
		t.Run(name, func(t *testing.T) {
			h := mk()
			var buf strings.Builder
			h.logSink, h.logLevel = &buf, slog.LevelWarn
			err := h.run("unattended")
			if !errors.Is(err, apperr.ErrNotRetried) || apperr.ExitCode(err) != 1 {
				t.Fatalf("err = %v (exit %d), want exit 1\n%s", err, apperr.ExitCode(err), h.output())
			}
			var found bool
			for _, l := range strings.Split(buf.String(), "\n") {
				if strings.Contains(l, "level=WARN") && strings.Contains(l, "not trying again without asking") {
					found = true
				}
			}
			if !found {
				t.Errorf("no WARN line about the skipped retry at logging.level warn:\n%s", buf.String())
			}
			h.mustContain("not trying again without asking")
		})
	}
}

// With a readable log and no earlier attempt, an unattended run still tries once.
func TestReadableOwnLogLetsAnUnattendedRunTryOnce(t *testing.T) {
	h := finishHarness(t)
	if err := h.run("unattended"); err != nil || h.updates != 1 {
		t.Fatalf("err=%v updates=%d\n%s", err, h.updates, h.output())
	}
	h.mustContain("Unfinished update finished and verified.")
}

// ---- R2-N2 / R3-S1: the ARM64 check and the R offer ----

// An up-to-date interactive run on an ARM64 machine without the tools: in
// stop mode no repair is offered (it would stop after the R), a line says why,
// and the run ends as "up to date", exit 0. Nothing was going to be closed.
func TestARM64StopModeOffersNoRepairAndEndsUpToDate(t *testing.T) {
	a := armTest(t)
	upToDate(a.harness)
	a.con.Terminal = true
	a.cfg.Preflight.ARM64Toolchain = config.PreflightStop
	a.con.Keys = []platform.Key{key('r')}
	if err := a.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, a.output())
	}
	a.mustContain("Hermes is up to date", "A repair is not offered: the ARM64 build tools are missing", "preflight.arm64_toolchain is stop", "winget install LLVM.LLVM")
	if strings.Contains(a.output(), "Press R") || strings.Contains(a.output(), "Stopping before anything is closed") || len(a.con.Keys) != 1 || a.updates != 0 || len(a.app.Calls()) != 0 {
		t.Errorf("the repair was offered or the run stopped (keys left %d):\n%s", len(a.con.Keys), a.output())
	}
}

// Stop mode with the tools in place: the offer is made as before, and the
// toolchain is probed once for the whole run.
func TestARM64StopModeWithToolsStillOffersTheRepair(t *testing.T) {
	a := armTest(t)
	upToDate(a.harness)
	a.con.Terminal = true
	a.cfg.Preflight.ARM64Toolchain = config.PreflightStop
	a.files[filepath.Join("vs", "BuildTools", "VC", "Tools", "Llvm", "ARM64", "bin", "clang.exe")] = true
	a.con.Keys = []platform.Key{key('r')}
	if err := a.run(); err != nil || a.updates != 1 {
		t.Fatalf("err=%v updates=%d\n%s", err, a.updates, a.output())
	}
	a.mustContain("Press R to repair this install")
	if n := a.vswhereCalls(); n != 1 || strings.Contains(a.output(), "ARM64") {
		t.Errorf("probed %d times, want 1; output mentions ARM64:\n%s", n, a.output())
	}
}

// In warn mode the offer is made without any ARM64 text: another key leaves
// everything as it is, and the toolchain was never looked at for nothing
// printed. After the R the warning comes once, with the run.
func TestARM64WarnModeSaysNothingUntilTheRepairIsAsked(t *testing.T) {
	a := armTest(t)
	upToDate(a.harness)
	a.con.Terminal = true
	a.con.Keys = []platform.Key{key('x')}
	if err := a.run(); err != nil || a.updates != 0 {
		t.Fatalf("err=%v updates=%d\n%s", err, a.updates, a.output())
	}
	a.mustContain("Press R to repair this install", "Hermes is up to date")
	if strings.Contains(a.output(), "ARM64") || strings.Contains(a.output(), "Visual Studio") {
		t.Errorf("an up-to-date run that repairs nothing printed the ARM64 warning:\n%s", a.output())
	}
	a = armTest(t)
	upToDate(a.harness)
	a.con.Terminal = true
	a.con.Keys = []platform.Key{key('r')}
	if err := a.run(); err != nil || a.updates != 1 {
		t.Fatalf("after R: err=%v updates=%d\n%s", err, a.updates, a.output())
	}
	out := a.output()
	if n := strings.Count(out, "clang.exe not found"); n != 1 || strings.Index(out, "clang.exe not found") < strings.Index(out, "Press R") {
		t.Errorf("the warning must come once, after the offer (%d times):\n%s", n, out)
	}
	if n := a.vswhereCalls(); n != 1 {
		t.Errorf("the toolchain was probed %d times after R, want 1", n)
	}
}

// After the R in stop mode (tools present) nothing changes; and a stop-mode
// repair that was asked for with `repair` still stops, as before.
func TestARM64StopModeStopsTheRepairCommand(t *testing.T) {
	a := armTest(t)
	upToDate(a.harness)
	a.cfg.Preflight.ARM64Toolchain = config.PreflightStop
	a.con.Keys = []platform.Key{key('y')}
	a.repair = true
	stoppedBeforeAnythingChanged(t, a.harness, a.run())
	a.mustContain("clang.exe not found", "preflight.arm64_toolchain: stop")
}

// A run that finishes an unfinished update runs the dependency step too.
func TestARM64StopAppliesToAFinishingRun(t *testing.T) {
	a := armTest(t)
	upToDate(a.harness)
	a.receipts = &fakeReceipts{prev: receiptDied()}
	a.cfg.Preflight.ARM64Toolchain = config.PreflightStop
	a.con.Keys = []platform.Key{key('y')}
	stoppedBeforeAnythingChanged(t, a.harness, a.run())
	a.mustContain("clang.exe not found")
}

// ---- R2-N3: no repair offer inside a Hermes chat (the parent chain) ----

func TestRepairIsNotOfferedWhenAnAncestorIsTheGateway(t *testing.T) {
	for name, parent := range map[string]platform.Process{"gateway": gatewayProc(), "backend": backendProc()} {
		t.Run(name, func(t *testing.T) {
			h := offerHarness(t)
			h.chatChain(parent) // the updater runs under a shell under the gateway
			h.con.Keys = []platform.Key{key('r')}
			if err := h.run(); err != nil {
				t.Fatalf("err = %v\n%s", err, h.output())
			}
			h.mustContain("A repair is not offered: this run is inside a Hermes chat", "Hermes is up to date")
			if strings.Contains(h.output(), "Press R") || len(h.con.Keys) != 1 || h.updates != 0 || len(h.app.Calls()) != 0 {
				t.Errorf("offered a repair inside a chat (keys left %d):\n%s", len(h.con.Keys), h.output())
			}
		})
	}
	// a plain terminal (no such ancestor) is offered it
	h := offerHarness(t)
	h.chatChain(platform.Process{PID: 50, Name: "explorer.exe", Created: h.clock.Now()})
	h.con.Keys = []platform.Key{key('x')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	h.mustContain("Press R to repair this install")
}

// ---- R2-N7: the stage is worded as a phrase ----

func TestStopStagesAreWordedAsPhrasesNotLabels(t *testing.T) {
	for k, want := range map[string]string{
		"gateway_stop": "pausing the gateway",
		"fetch":        "fetching the new commits",
		"pull":         "pulling the new code",
		"pydeps":       "installing Python dependencies",
		"frontend":     "building the TUI and web UI",
		"desktop":      "building the desktop app",
		"package":      "packaging the desktop app",
		"finalize":     "syncing skills and config",
		"restart":      "restarting the gateway",
		"build":        "building the apps",
		"tail":         "the last steps",
		"start":        "starting the update",
		"":             "starting the update",
	} {
		if got := stepWords(k); got != want {
			t.Errorf("stepWords(%q) = %q, want %q", k, got, want)
		}
	}
	// through a receipt: its last recorded stage is "plan", so it was pulling
	h := finishHarness(t)
	h.receipts = &fakeReceipts{prev: &hermes.PreviousUpdate{Signals: []string{hermes.SignalReceiptInterrupted}, At: crashedAt, StepKey: hermes.ReceiptStopStep("plan")}}
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	h.mustContain("stopped during pulling the new code at yesterday 8:56 PM")
	if strings.Contains(h.output(), "clearing caches") {
		t.Errorf("the step label leaked into the sentence:\n%s", h.output())
	}
}
