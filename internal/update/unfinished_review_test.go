package update

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
	"github.com/cloudn8ive/hermes-safe-update/internal/ui"
)

// The review of v0.5.0 (S2-S5, N1, N2, N5, N6, N8, 8c): synthetic logs and
// receipts only, fake processes and consoles.

// failedRun is a run that reached `hermes update`, which stopped at once, and
// that wrote its "done" line with a failing exit.
func failedRun(ts time.Time, pid int) string {
	s := logLine(ts, logStarted, "pid="+strconv.Itoa(pid), "check=false", "unattended=true")
	s += logLine(ts.Add(time.Second), "== Running: hermes update --yes --keep-stash --branch main")
	s += logLine(ts.Add(2*time.Second), "  | ✗ A git rebase is in progress")
	return s + logLine(ts.Add(3*time.Second), logDone, "exit=6")
}

// triedRun is a run that set out to finish the update with the given keys and
// stopped before `hermes update` (it has no Running line).
func triedRun(ts time.Time, pid int, keys string) string {
	s := logLine(ts, logStarted, "pid="+strconv.Itoa(pid), "check=false", "unattended=true")
	s += logLine(ts.Add(time.Second), logFinishing, "keys="+keys)
	return s + logLine(ts.Add(2*time.Second), logDone, "exit=1")
}

const receiptKey = "receipt:0f1e2d3c4b5a69780f1e2d3c4b5a6978"

func receiptDiedWithKey() *hermes.PreviousUpdate {
	p := receiptDied()
	p.Keys = []string{receiptKey}
	return p
}

// logOf serves a log tail by path: the current file and the rotated one.
func logOf(current, rotated string, reads *[]string) func(string, int64) ([]byte, error) {
	return func(path string, _ int64) ([]byte, error) {
		*reads = append(*reads, path)
		if strings.HasSuffix(path, ".1") {
			if rotated == "" {
				return nil, os.ErrNotExist
			}
			return []byte(rotated), nil
		}
		return []byte(current), nil
	}
}

// ---- S2: an unfinished update is not offered again and again ----

// The receipt is older than a later run that went through `hermes update`
// (and wrote its done line, however it ended): that run has run over it.
func TestReceiptOlderThanALaterUpdateRunIsNotCounted(t *testing.T) {
	h := finishHarness(t)
	h.logTail = func(string, int64) ([]byte, error) {
		return []byte(failedRun(crashedAt.Add(time.Hour), 5001) + ownStart(crashedAt.Add(2*time.Hour))), nil
	}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	h.mustContain("Hermes is up to date")
	if h.updates != 0 || len(h.app.Calls()) != 0 || strings.Contains(h.output(), "last update") {
		t.Errorf("a later run already went through hermes update: updates=%d app=%v\n%s", h.updates, h.app.Calls(), h.output())
	}
	// a run before the stop does not supersede it
	h = finishHarness(t)
	h.logTail = func(string, int64) ([]byte, error) {
		return []byte(failedRun(crashedAt.Add(-time.Hour), 5001) + ownStart(crashedAt.Add(2*time.Hour))), nil
	}
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil || h.updates != 1 {
		t.Errorf("an older run hid the unfinished update: err=%v updates=%d\n%s", err, h.updates, h.output())
	}
}

// The scheduled task: the receipt stays "interrupted" because the finishing
// update stops early, so run after run would close Hermes for nothing.
func TestUnattendedRunsFinishAnUnfinishedUpdateOnlyOnce(t *testing.T) {
	var buf strings.Builder
	mk := func() *harness {
		h := finishHarness(t) // the receipt fixture persists
		h.onUpdate(1, "✗ A git rebase is in progress", false)
		h.logSink = &buf
		var reads []string
		h.logTail = func(p string, n int64) ([]byte, error) {
			if strings.HasSuffix(p, ".1") {
				return nil, os.ErrNotExist
			}
			return logOf(buf.String(), "", &reads)(p, n)
		}
		return h
	}
	h1 := mk()
	if err := h1.run("unattended"); !errors.Is(err, apperr.ErrUpdateProblem) || h1.updates != 1 || len(h1.app.Calls()) == 0 {
		t.Fatalf("first run: err=%v updates=%d app=%v\n%s", err, h1.updates, h1.app.Calls(), h1.output())
	}
	h2 := mk()
	if err := h2.run("unattended"); err != nil {
		t.Fatalf("second run: err = %v\n%s", err, h2.output())
	}
	if h2.updates != 0 || len(h2.app.Calls()) != 0 {
		t.Errorf("the second run closed Hermes again: updates=%d app=%v\n%s", h2.updates, h2.app.Calls(), h2.output())
	}
	// the first run reached `hermes update`, so the receipt is older than it: the
	// signal is retired and the run says what is true, that nothing is waiting
	h2.mustContain("Hermes is up to date")
}

// A run writes the line that tells a later run which unfinished update it
// tried; read back, it names the same keys.
func TestFinishingAttemptIsLoggedAndReadBack(t *testing.T) {
	h := finishHarness(t)
	h.receipts = &fakeReceipts{prev: receiptDiedWithKey()}
	var buf strings.Builder
	h.logSink = &buf
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `msg="finishing the previous update" keys=`+receiptKey) {
		t.Fatalf("no attempt line:\n%s", buf.String())
	}
	var kept []string // the run stopped before hermes update: no Running line, no done line
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.Contains(l, "== Running") || strings.Contains(l, "==== done") {
			continue
		}
		kept = append(kept, strings.Replace(l, "pid="+strconv.Itoa(os.Getpid()), "pid="+strconv.Itoa(pidOld), 1))
	}
	v, err := readLog([]byte(strings.Join(kept, "\n")+"\n"+ownStart(time.Now())), os.Getpid(), nil)
	if err != nil || !v.Tried[receiptKey] {
		t.Errorf("read back %+v, %v", v, err)
	}
}

// Rule 2 alone: the earlier attempt left no Running line (it stopped before
// hermes update), so only the attempt line tells.
func TestEarlierAttemptStopsUnattendedRunsButNotAPersonWhoIsAsked(t *testing.T) {
	log := triedRun(t0, pidOld, receiptKey) + ownStart(t0.Add(time.Hour))
	mk := func(keys string) *harness {
		h := finishHarness(t)
		h.receipts = &fakeReceipts{prev: receiptDiedWithKey()}
		l := strings.Replace(log, receiptKey, keys, 1)
		h.logTail = func(p string, _ int64) ([]byte, error) {
			if strings.HasSuffix(p, ".1") {
				return nil, os.ErrNotExist
			}
			return []byte(l), nil
		}
		return h
	}
	for _, flag := range []string{"unattended", "yes"} {
		t.Run(flag, func(t *testing.T) {
			h := mk(receiptKey)
			cards := h.withCards()
			// exit 1: nothing failed in this run, but the work is still owed
			if err := h.run(flag); !errors.Is(err, apperr.ErrNotRetried) || apperr.ExitCode(err) != apperr.Failure {
				t.Fatalf("err = %v (exit %d), want ErrNotRetried, exit 1\n%s", err, apperr.ExitCode(err), h.output())
			}
			h.mustContain("an earlier attempt to finish it failed; not trying again without asking")
			if row, ok := summaryRow(*cards, "Not retried"); !ok || !strings.Contains(row.Value, "an earlier attempt to finish it failed") {
				t.Errorf("card row = %+v, %v", row, ok)
			}
			if h.updates != 0 || len(h.app.Calls()) != 0 {
				t.Errorf("tried again: updates=%d app=%v", h.updates, h.app.Calls())
			}
			// the run exits 0 but does not call this "up to date" (R2-N1)
			if strings.Contains(h.output(), "Hermes is up to date") {
				t.Errorf("an unfinished update that was not retried is not \"up to date\":\n%s", h.output())
			}
			h.mustContain("was not retried: nothing was changed", "Not retried", "run Hermes Safe Update from the Start menu or a terminal")
		})
	}
	// another unfinished update is a new case: one attempt
	h := mk("receipt:ffffffffffffffffffffffffffffffff")
	if err := h.run("unattended"); err != nil || h.updates != 1 {
		t.Errorf("a different update was not tried: err=%v updates=%d", err, h.updates)
	}
	// --force goes on anyway
	h = mk(receiptKey)
	if err := h.run("unattended", "force"); err != nil || h.updates != 1 {
		t.Errorf("--force did not go on: err=%v updates=%d", err, h.updates)
	}
	// the repair command asks the person (or goes on with --unattended): it is asked for
	h = mk(receiptKey)
	h.repair = true
	if err := h.run("unattended"); err != nil || h.updates != 1 {
		t.Errorf("repair did not go on: err=%v updates=%d", err, h.updates)
	}
	// a person who is asked decides, and is told the earlier attempt failed
	h = mk(receiptKey)
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil || h.updates != 1 {
		t.Fatalf("interactive: err=%v updates=%d\n%s", err, h.updates, h.output())
	}
	h.mustContain("The last update did not finish (" + crashedWords + "), and an earlier attempt to finish it failed. Hermes will close to finish it")
	// the row says so too, in check mode as well
	h = mk(receiptKey)
	h.mode = ModeCheck
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	h.mustContain("did not finish (" + crashedWords + "); an earlier attempt to finish it failed; run Hermes Safe Update to finish it")
}

// ---- S3: a finishing run that fails leaves a tree nobody can vouch for ----

func TestFinishingRunThatFailsDoesNotStartTheGateway(t *testing.T) {
	for _, tc := range []struct {
		name      string
		setup     func(h *harness)
		wantStart int
	}{
		{"finishing an unfinished update", func(h *harness) {}, 0},
		{"repair", func(h *harness) { h.receipts = nil; h.repair = true }, 1},
		// the repair command meets an unfinished update: it finishes it (no
		// plain repair), so the same guard applies
		{"repair command with an unfinished update", func(h *harness) { h.repair = true }, 0},
		{"no previous update, update fails before HEAD moves", func(h *harness) { h.receipts = nil; h.mainCheck = "3 commits behind origin/main" }, 1},
		// the crash left a half-updated tree, and the update with the new
		// commits fails before HEAD moves (the network is down at the fetch)
		{"unfinished update with new commits, update fails before HEAD moves", func(h *harness) { h.mainCheck = "3 commits behind origin/main" }, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := finishHarness(t)
			tc.setup(h)
			cards := h.withCards()
			var run *Run
			h.onRun = func(r *Run) { run = r }
			h.update = func(int) execx.Result { // HEAD stays where it was, exit 1, no "Update complete!"
				hermesPausesGateway(h)
				return execx.Result{Code: 1, Output: "→ Updating Python dependencies…\n✗ build failed"}
			}
			h.gatewayComesBackOnNewCode()
			h.con.Keys = []platform.Key{key('y')}
			_ = h.run()
			if h.gw.starts != tc.wantStart {
				t.Fatalf("starts = %d, want %d\n%s", h.gw.starts, tc.wantStart, h.output())
			}
			row, ok := summaryRow(*cards, "Gateway rescue")
			if tc.wantStart == 0 {
				if !ok || !strings.Contains(row.Value, "not started: the update that did not finish could not be finished, so the tree may still be half-updated") || !strings.Contains(row.Value, "run `hermes update` again") {
					t.Errorf("row = %+v, %v", row, ok)
				}
				if run.Facts.GW.NotStarted == "" {
					t.Error("GW.NotStarted is empty")
				}
			} else if ok && strings.Contains(row.Value, "not started") {
				t.Errorf("row = %+v", row)
			}
		})
	}
}

// ---- S4: the run that stopped is the process that wrote the line, not any process with its pid ----

func TestLogRunIsAliveOnlyWhileItsOwnProcessLives(t *testing.T) {
	tail := crashedRun(t0, "→ Packaging the desktop app…") + ownStart(t0.Add(time.Hour))
	alive := func(pid int) bool { return pid == pidOld }
	created := func(at time.Time, ok bool) func(int) (time.Time, bool) {
		return func(int) (time.Time, bool) { return at, ok }
	}
	for name, tc := range map[string]struct {
		created func(int) (time.Time, bool)
		want    bool // unfinished
	}{
		"created after the run began (a reused pid)":    {created(t0.Add(30*time.Minute), true), true},
		"created before the run began (the run itself)": {created(t0.Add(-time.Hour), true), false},
		"created within the slack of the started line":  {created(t0.Add(time.Second), true), false},
		"creation time unknown: not proven alive":       {created(time.Time{}, false), true},
		"no lookup at all: not proven alive":            {nil, true},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := previousInLog([]byte(tail), os.Getpid(), runOwner(alive, tc.created))
			if err != nil || (p != nil) != tc.want {
				t.Errorf("previousInLog = %v, %v; want unfinished=%v", p, err, tc.want)
			}
		})
	}
	// a pid that is not alive is gone whatever the creation time says
	p, _ := previousInLog([]byte(tail), os.Getpid(), runOwner(func(int) bool { return false }, created(t0.Add(-time.Hour), true)))
	if p == nil {
		t.Error("a dead pid counted as the run")
	}
}

func TestLogRunWithThisProcessesPidIsNeverItsOwner(t *testing.T) {
	older := logLine(t0, logStarted, "pid="+strconv.Itoa(os.Getpid()), "check=false") +
		logLine(t0.Add(time.Second), "== Running: hermes update --yes --keep-stash --branch main") +
		logLine(t0.Add(2*time.Second), "  | → Packaging the desktop app…")
	tail := older + ownStart(t0.Add(time.Hour))
	// every pid is alive and was created long ago: only the self rule can say no
	owner := runOwner(func(int) bool { return true }, func(int) (time.Time, bool) { return t0.Add(-time.Hour), true })
	p, err := previousInLog([]byte(tail), os.Getpid(), owner)
	if err != nil || p == nil || p.Stage != "packaging the desktop app" {
		t.Errorf("previousInLog = %v, %v; the older run with this pid is not alive just because this process is", p, err)
	}
}

// ---- S5: nobody can type where stdin is not a console ----

func TestRepairIsNotOfferedWhenStdinIsNotAConsole(t *testing.T) {
	h := offerHarness(t)
	h.con.NoInputTerminal = true
	h.con.Keys = []platform.Key{key('r')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(h.output(), "Press R") || len(h.con.Keys) != 1 {
		t.Errorf("a prompt was shown on a console nobody can type into (keys left %d):\n%s", len(h.con.Keys), h.output())
	}
	h.mustContain("Hermes is up to date")
	if h.updates != 0 || len(h.app.Calls()) != 0 {
		t.Errorf("changed something: updates=%d", h.updates)
	}
}

// ---- N1: no offer that cannot work ----

func TestRepairIsNotOfferedWhenTheRunWouldStopForTheDisk(t *testing.T) {
	h := offerHarness(t)
	h.plat.Disk.(*platform.FakeDisk).FreeBytes = 2e9
	h.con.Keys = []platform.Key{key('r')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	h.mustContain("not enough free disk space", "A repair is not offered: there is not enough free disk space for the desktop rebuild", "Hermes is up to date")
	if strings.Contains(h.output(), "Press R") || len(h.con.Keys) != 1 || h.updates != 0 {
		t.Errorf("offered a repair that stops at once (keys left %d):\n%s", len(h.con.Keys), h.output())
	}
}

func TestRepairIsNotOfferedInsideAHermesChat(t *testing.T) {
	h := offerHarness(t)
	h.env = map[string]string{"HERMES_SESSION_ID": "x"}
	h.con.Keys = []platform.Key{key('r')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	h.mustContain("A repair is not offered: this run is inside a Hermes chat")
	if strings.Contains(h.output(), "Press R") || len(h.con.Keys) != 1 || h.updates != 0 {
		t.Errorf("offered a repair inside a chat:\n%s", h.output())
	}
}

// ---- N2: "any other key exits" is true ----

func TestRepairOfferEveryKeyButRExits(t *testing.T) {
	for name, k := range map[string]platform.Key{
		"arrow": {Other: true}, "tab": key('\t'), "punctuation": key('.'), "backspace": key(0x7f), "letter": key('x'), "enter": key('\r'),
	} {
		t.Run(name, func(t *testing.T) {
			h := offerHarness(t)
			var buf strings.Builder
			h.logSink = &buf
			h.con.Keys = []platform.Key{k}
			if err := h.run(); err != nil {
				t.Fatalf("err = %v", err)
			}
			if !strings.Contains(buf.String(), "timeout=false") || strings.Contains(buf.String(), "timeout=true") {
				t.Errorf("the key did not end the window (it waited for the timeout):\n%s", buf.String())
			}
			h.mustContain("Hermes is up to date")
			if h.updates != 0 {
				t.Errorf("updates = %d", h.updates)
			}
		})
	}
}

// ---- N5: the run that stopped sits in the rotated log ----

func TestRotatedLogIsReadWhenTheCurrentOneHasNoEarlierRun(t *testing.T) {
	h := finishHarness(t)
	h.receipts = nil
	var reads []string
	h.logTail = logOf(ownStart(t0.Add(40*time.Hour)), crashedRun(crashedAt.Add(-3*time.Minute), "→ Packaging the desktop app…"), &reads)
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	h.mustContain("did not finish (stopped during packaging the desktop app at", "Unfinished update finished and verified.")
	// a current log that has an earlier run is the whole story: .1 is not looked at
	h = finishHarness(t)
	h.receipts = nil
	reads = nil
	h.logTail = logOf(finishedRun(t0, pidOld)+ownStart(t0.Add(time.Hour)), crashedRun(t0.Add(-48*time.Hour), "→ Packaging the desktop app…"), &reads)
	if err := h.run(); err != nil || h.updates != 0 {
		t.Fatalf("err=%v updates=%d\n%s", err, h.updates, h.output())
	}
	for _, p := range reads {
		if strings.HasSuffix(p, ".1") {
			t.Errorf("the rotated log was read although the current one has an earlier run: %v", reads)
		}
	}
}

// ---- N6: a key typed ahead answers nothing ----

func TestKeysTypedDuringThePreflightDoNotAnswerTheRepairOffer(t *testing.T) {
	h := offerHarness(t)
	h.con.TypeAhead = []platform.Key{key('r')}
	h.con.Keys = []platform.Key{key('x')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	if h.updates != 0 || len(h.app.Calls()) != 0 || strings.Contains(h.output(), "Repair finished") {
		t.Errorf("a key typed ahead repaired the install: updates=%d\n%s", h.updates, h.output())
	}
	if h.con.Drains == 0 {
		t.Error("the input was not drained")
	}
}

func TestKeysTypedDuringThePreflightDoNotAnswerTheClosePrompt(t *testing.T) {
	h := newPreflightHarness(t)
	h.con.TypeAhead = []platform.Key{key('y')}
	h.con.Keys = []platform.Key{key('n')}
	if err := h.run(); !errors.Is(err, apperr.ErrCancelled) || h.updates != 0 || len(h.app.Calls()) != 0 {
		t.Errorf("err=%v updates=%d: a typed-ahead y closed Hermes\n%s", err, h.updates, h.output())
	}
}

// ---- N8: one wording for both signals ----

func TestReceiptAndLogSayTheSameStep(t *testing.T) {
	// the log reads the step from the output of hermes update ...
	for line, stage := range map[string]string{
		"→ Pulling updates…":                 "plan",
		"Installing Python dependencies…":    "apply",
		"Building desktop packaged app…":     "",
		"→ Packaging the desktop app…":       "",
		"Update complete! (v1.0 → v1.1)":     "",
		"Stopping Windows gateway (pid 1)…":  "",
		"Preparing Node dependencies…":       "",
		"→ Fetching updates from origin":     "",
		"Code updated!":                      "",
		"Building the web UI…":               "",
		"Resolving Python dependencies…":     "",
		"Updating Python dependencies (uv)…": "",
	} {
		logWords := stepWords(hermes.TriggerFor(line))
		if logWords == "" || strings.Contains(logWords, "  ") {
			t.Errorf("log words for %q = %q", line, logWords)
		}
		if stage == "" {
			continue
		}
		// ... the receipt from the last stage it recorded: the same words
		if got := stepWords(hermes.ReceiptStopStep(stage)); got != logWords {
			t.Errorf("receipt stage %q says %q, the log line %q says %q", stage, got, line, logWords)
		}
	}
	// a receipt that knows no more than "the builds" keeps the group word
	if got := stepWords(hermes.ReceiptStopStep("deps")); got != "building the apps" {
		t.Errorf("deps -> %q", got)
	}
}

func TestReceiptSignalIsWordedLikeTheLogSignal(t *testing.T) {
	h := finishHarness(t)
	h.receipts = &fakeReceipts{prev: &hermes.PreviousUpdate{Signals: []string{hermes.SignalReceiptInterrupted}, At: crashedAt, StepKey: "pydeps"}}
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	h.mustContain("did not finish (stopped during installing Python dependencies at yesterday 8:56 PM)")
}

// ---- 8c: advice for a desktop app that stays unverified ----

func TestUnverifiedDesktopAfterAFinishingOrRepairRunNamesTheForceBuild(t *testing.T) {
	const want = "hermes desktop --force-build"
	for name, setup := range map[string]func(h *harness){
		"finishing": func(h *harness) {},
		"repair":    func(h *harness) { h.receipts = nil; h.repair = true },
	} {
		t.Run(name, func(t *testing.T) {
			h := finishHarness(t)
			setup(h)
			cards := h.withCards()
			h.verifier = fakeVerifier{ok: false, out: "no packaged app found"}
			h.con.Keys = []platform.Key{key('y')}
			if err := h.run(); !errors.Is(err, apperr.ErrUpdateProblem) {
				t.Fatalf("err = %v\n%s", err, h.output())
			}
			row, ok := summaryRow(*cards, "Desktop rebuild")
			if !ok || !strings.Contains(row.Value, want) || row.Verdict == nil || *row.Verdict != ui.VerdictAttention {
				t.Errorf("row = %+v, %v", row, ok)
			}
			for _, c := range h.runner.Calls() {
				if strings.Contains(strings.Join(c.Argv, " "), "force-build") {
					t.Errorf("Safe Update ran it: %v", c.Argv)
				}
			}
		})
	}
	// a verified run, and a normal update that fails to verify, get no such row
	h := finishHarness(t)
	cards := h.withCards()
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if _, ok := summaryRow(*cards, "Desktop rebuild"); ok {
		t.Error("a verified run got the advice")
	}
	h = newPreflightHarness(t)
	cards = h.withCards()
	h.verifier = fakeVerifier{ok: false, out: "no packaged app found"}
	h.con.Keys = []platform.Key{key('y')}
	_ = h.run()
	if _, ok := summaryRow(*cards, "Desktop rebuild"); ok {
		t.Error("a normal update got the advice")
	}
}
