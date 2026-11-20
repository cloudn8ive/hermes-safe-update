package update

import (
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
	"github.com/cloudn8ive/hermes-safe-update/internal/timings"
)

// ---- fakes and fixtures ----

type fakeReceipts struct {
	prev  *hermes.PreviousUpdate
	err   error
	calls int
	// finished is when the newest update that ran to its end began (zero:
	// none), finishedErr why that cannot be read.
	finished    time.Time
	finishedErr error
}

func (f *fakeReceipts) Unfinished() (*hermes.PreviousUpdate, error) {
	f.calls++
	return f.prev, f.err
}

func (f *fakeReceipts) NewestFinished() (time.Time, error) { return f.finished, f.finishedErr }

// crashedAt is when the fake update before this run stopped; the harness
// clock reads 2026-10-01 10:00 UTC, so it was "yesterday 8:56 PM".
var crashedAt = time.Date(2026, 9, 30, 20, 56, 10, 0, time.UTC)

const crashedWords = "stopped during building the apps at yesterday 8:56 PM"

func receiptDied() *hermes.PreviousUpdate {
	return &hermes.PreviousUpdate{Signals: []string{hermes.SignalReceiptRunning}, At: crashedAt, Stage: "building the apps"}
}

// logLine is one line of the Safe Update log, in slog's text format.
func logLine(ts time.Time, msg string, attrs ...string) string {
	s := "time=" + ts.Format("2006-01-02T15:04:05.000Z07:00") + " level=INFO msg=" + strconv.Quote(msg)
	for _, a := range attrs {
		s += " " + a
	}
	return s + "\n"
}

const pidOld = 4711 // the run before this one (a pid that is not this test process)

// ownStart is the "started" line of the run that is looking (this process).
func ownStart(ts time.Time) string {
	return logLine(ts, logStarted, "pid="+strconv.Itoa(os.Getpid()), "check=false", "unattended=false")
}

// crashedRun is a run that reached `hermes update`, printed the given
// output lines and then died: no "done" line.
func crashedRun(ts time.Time, out ...string) string {
	s := logLine(ts, logStarted, "pid="+strconv.Itoa(pidOld), "check=false", "unattended=false")
	s += logLine(ts.Add(time.Second), "prompt: Hermes will close for the update.")
	s += logLine(ts.Add(2*time.Second), "== Running: hermes update --yes --keep-stash --branch main")
	for i, l := range out {
		s += logLine(ts.Add(time.Duration(3+i)*time.Second), "  | "+l)
	}
	return s
}

func finishedRun(ts time.Time, pid int) string {
	s := logLine(ts, logStarted, "pid="+strconv.Itoa(pid), "check=false", "unattended=false")
	s += logLine(ts.Add(time.Second), "== Running: hermes update --yes --keep-stash --branch main")
	s += logLine(ts.Add(2*time.Second), "  | Update complete! (v1 → v2)")
	return s + logLine(ts.Add(3*time.Second), logDone, "exit=0")
}

func upToDateRun(ts time.Time, pid int) string {
	s := logLine(ts, logStarted, "pid="+strconv.Itoa(pid), "check=false", "unattended=false")
	s += logLine(ts.Add(time.Second), "Nothing to update (use --force to run anyway).")
	return s + logLine(ts.Add(2*time.Second), logDone, "exit=0")
}

var t0 = crashedAt.Add(-5 * time.Minute)

// ---- the own-log signal ----

func TestLogRunThatReachedTheUpdateAndNeverWroteDoneIsUnfinished(t *testing.T) {
	tail := crashedRun(t0, "→ Fetching updates...", "Building desktop packaged app…", "→ Packaging the desktop app…") + ownStart(t0.Add(time.Hour))
	p, err := previousInLog([]byte(tail), os.Getpid(), nil)
	if err != nil || p == nil {
		t.Fatalf("previousInLog = %v, %v", p, err)
	}
	if len(p.Signals) != 1 || p.Signals[0] != hermes.SignalOwnLog {
		t.Errorf("signals = %v", p.Signals)
	}
	if p.Stage != "packaging the desktop app" {
		t.Errorf("stage = %q (the last progress line is the packaging one)", p.Stage)
	}
	if want := t0.Add(5 * time.Second); !p.At.Equal(want) {
		t.Errorf("at = %s, want the time of its last line %s", p.At, want)
	}
}

func TestLogRunWithDoneIsFinished(t *testing.T) {
	tail := finishedRun(t0, pidOld) + ownStart(t0.Add(time.Hour))
	if p, err := previousInLog([]byte(tail), os.Getpid(), nil); err != nil || p != nil {
		t.Errorf("a finished run: %v, %v", p, err)
	}
}

// A run that only got through the pre-flight (a stop, a refusal, a check)
// never started the update: nothing is owed.
func TestLogRunThatOnlyReachedThePreflightIsNotUnfinished(t *testing.T) {
	tail := logLine(t0, logStarted, "pid="+strconv.Itoa(pidOld), "check=false") +
		logLine(t0.Add(time.Second), "== Pre-flight") + logLine(t0.Add(2*time.Second), "prompt: Hermes will close for the update.") +
		ownStart(t0.Add(time.Hour))
	if p, err := previousInLog([]byte(tail), os.Getpid(), nil); err != nil || p != nil {
		t.Errorf("pre-flight only: %v, %v", p, err)
	}
}

// Only a run before this one counts: this run's own lines are never read as
// the run that stopped (and a pid that is still alive is still running).
func TestLogOwnRunAndLiveRunsAreNotUnfinished(t *testing.T) {
	own := ownStart(t0) + logLine(t0.Add(time.Second), "== Running: hermes update --yes")
	if p, err := previousInLog([]byte(own), os.Getpid(), nil); err != nil || p != nil {
		t.Errorf("this run's own lines: %v, %v", p, err)
	}
	live := crashedRun(t0, "→ Fetching updates...") + ownStart(t0.Add(time.Hour))
	alive := func(pid int, _ time.Time) bool { return pid == pidOld }
	if p, err := previousInLog([]byte(live), os.Getpid(), alive); err != nil || p != nil {
		t.Errorf("the other run is alive: %v, %v", p, err)
	}
	dead := func(int, time.Time) bool { return false }
	if p, err := previousInLog([]byte(live), os.Getpid(), dead); err != nil || p == nil {
		t.Errorf("the other run is gone: %v, %v", p, err)
	}
}

// The incident: after the crash two runs said "up to date" and exited. They
// wrote "done" but never ran the update, so the crashed run is still the one
// that did not finish.
func TestLogUnfinishedRunSurvivesLaterUpToDateRuns(t *testing.T) {
	tail := crashedRun(t0, "→ Packaging the desktop app…") + upToDateRun(t0.Add(time.Hour), 5001) + upToDateRun(t0.Add(2*time.Hour), 5002) + ownStart(t0.Add(3*time.Hour))
	p, err := previousInLog([]byte(tail), os.Getpid(), nil)
	if err != nil || p == nil || p.Stage != "packaging the desktop app" {
		t.Errorf("got %v, %v", p, err)
	}
	// ... until a later run finished an update
	tail = crashedRun(t0, "→ Packaging the desktop app…") + finishedRun(t0.Add(time.Hour), 5001) + ownStart(t0.Add(3*time.Hour))
	if p, err := previousInLog([]byte(tail), os.Getpid(), nil); err != nil || p != nil {
		t.Errorf("a later update finished it: %v, %v", p, err)
	}
}

func TestLogThatDoesNotContainThisRunIsUnknown(t *testing.T) {
	tail := crashedRun(t0, "→ Packaging the desktop app…")
	if p, err := previousInLog([]byte(tail), os.Getpid(), nil); err == nil || p != nil {
		t.Errorf("the log is not written for this run: got %v, %v; that is unknown, never a verdict", p, err)
	}
	if p, err := previousInLog(nil, os.Getpid(), nil); err == nil || p != nil {
		t.Errorf("empty log: %v, %v", p, err)
	}
}

// The tail starts in the middle of a run (the file is long): lines before the
// first "started" line belong to a run whose start was cut off.
func TestLogTailCutInsideTheUnfinishedRun(t *testing.T) {
	full := crashedRun(t0, "→ Packaging the desktop app…")
	cut := full[strings.Index(full, "time=")+10:] // a first line without its start
	cut = cut[strings.Index(cut, "time="):]       // the next whole line
	p, err := previousInLog([]byte(cut+ownStart(t0.Add(time.Hour))), os.Getpid(), nil)
	if err != nil || p == nil {
		t.Fatalf("got %v, %v", p, err)
	}
}

func TestLogRunWithoutAProgressLineSaysItStartedTheUpdate(t *testing.T) {
	tail := crashedRun(t0) + ownStart(t0.Add(time.Hour))
	p, _ := previousInLog([]byte(tail), os.Getpid(), nil)
	if p == nil || p.Stage != "starting the update" {
		t.Errorf("got %+v", p)
	}
}

// A line that is not slog text (a half-written one, a stray one) is skipped.
func TestLogGarbageLinesAreSkipped(t *testing.T) {
	tail := "this is not a log line\ntime=garbage level=INFO msg=x\n" + crashedRun(t0, "→ Pulling updates…") + "time=2026-09-30T20:56:10Z level=INFO msg=\"unterminated\n" + ownStart(t0.Add(time.Hour))
	p, err := previousInLog([]byte(tail), os.Getpid(), nil)
	if err != nil || p == nil || p.Stage != "pulling the new code" {
		t.Errorf("got %+v, %v", p, err)
	}
}

func TestReadLogTailKeepsWholeLinesAndTheEnd(t *testing.T) {
	dir := t.TempDir()
	path := dir + string(os.PathSeparator) + "safe-update.log"
	lines := []string{"alpha line one", "bravo line two", "charlie line three"}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readLogTail(path, 1<<20)
	if err != nil || string(got) != strings.Join(lines, "\n")+"\n" {
		t.Errorf("whole file: %q, %v", got, err)
	}
	got, err = readLogTail(path, int64(len("charlie line three\n"))+3) // cuts into the bravo line
	if err != nil || string(got) != "charlie line three\n" {
		t.Errorf("tail: %q, %v (the cut line must be dropped)", got, err)
	}
	if _, err := readLogTail(dir+string(os.PathSeparator)+"missing.log", 10); !os.IsNotExist(err) {
		t.Errorf("missing file: %v", err)
	}
}

// What a real run writes is what the reader reads back: the marker lines and
// the progress lines come from the same run code, in slog's own format.
func TestLogLinesWrittenByARunAreReadBack(t *testing.T) {
	h := newHarness(t)
	var buf strings.Builder
	h.logSink = &buf
	h.onUpdate(0, "→ Fetching updates...\nBuilding desktop packaged app…\n→ Packaging the desktop app…", true)
	h.con.Keys = []platform.Key{key('y')}
	_ = h.run() // the verdict does not matter, the log does
	var kept []string
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.Contains(l, `msg="==== done"`) || strings.Contains(l, "msg==== done") {
			continue // this run "crashed" before it wrote done
		}
		kept = append(kept, strings.Replace(l, "pid="+strconv.Itoa(os.Getpid()), "pid="+strconv.Itoa(pidOld), 1))
	}
	if strings.Contains(strings.Join(kept, "\n"), "==== done") {
		t.Fatalf("the done line is still there:\n%s", buf.String())
	}
	tail := strings.Join(kept, "\n") + "\n" + ownStart(time.Now())
	p, err := previousInLog([]byte(tail), os.Getpid(), nil)
	if err != nil || p == nil || p.Stage != "packaging the desktop app" {
		t.Fatalf("read back %+v, %v from:\n%s", p, err, tail)
	}
	// and with the done line the same run is finished
	full := buf.String()
	full = strings.ReplaceAll(full, "pid="+strconv.Itoa(os.Getpid()), "pid="+strconv.Itoa(pidOld)) + ownStart(time.Now())
	if p, err := previousInLog([]byte(full), os.Getpid(), nil); err != nil || p != nil {
		t.Errorf("the finished run was read as unfinished: %v, %v", p, err)
	}
}

// ---- merging the signals ----

func TestMergeKeepsEverySignalAndPrefersTheLogsStopDetails(t *testing.T) {
	rec := &hermes.PreviousUpdate{Signals: []string{hermes.SignalReceiptRunning}, At: crashedAt, Stage: "building the apps"}
	lg := &hermes.PreviousUpdate{Signals: []string{hermes.SignalOwnLog}, At: crashedAt.Add(2 * time.Minute), Stage: "packaging the desktop app"}
	marker := &hermes.PreviousUpdate{Signals: []string{hermes.SignalStaleMarker}}
	got := mergePrevious([]*hermes.PreviousUpdate{rec, lg, marker})
	if strings.Join(got.Signals, ",") != "receipt-running,own-log,stale-marker" {
		t.Errorf("signals = %v", got.Signals)
	}
	if got.Stage != "packaging the desktop app" || !got.At.Equal(lg.At) {
		t.Errorf("stop details = %q at %s, want the log's", got.Stage, got.At)
	}
	// the marker alone knows neither
	only := mergePrevious([]*hermes.PreviousUpdate{marker})
	if only.Stage != "" || !only.At.IsZero() {
		t.Errorf("marker only: %+v", only)
	}
	// the receipt fills in what the log does not know
	bare := &hermes.PreviousUpdate{Signals: []string{hermes.SignalOwnLog}}
	got = mergePrevious([]*hermes.PreviousUpdate{rec, bare})
	if got.Stage != "building the apps" || !got.At.Equal(crashedAt) {
		t.Errorf("fallback to the receipt: %+v", got)
	}
	if mergePrevious(nil) != nil {
		t.Error("no signals must be no verdict")
	}
}

func TestWhenTextIsPlainEnglishRelativeToNow(t *testing.T) {
	now := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC) // a Thursday
	for _, c := range []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 10, 1, 8, 5, 0, 0, time.UTC), "today 8:05 AM"},
		{time.Date(2026, 9, 30, 20, 56, 0, 0, time.UTC), "yesterday 8:56 PM"},
		{time.Date(2026, 9, 27, 20, 57, 0, 0, time.UTC), "Sun 8:57 PM"},
		{time.Date(2026, 9, 24, 0, 30, 0, 0, time.UTC), "Sep 24, 12:30 AM"},
		{time.Date(2025, 12, 31, 23, 0, 0, 0, time.UTC), "Dec 31 2025, 11:00 PM"},
		{time.Date(2026, 10, 1, 5, 0, 0, 0, time.FixedZone("x", 7*3600)), "yesterday 10:00 PM"}, // 22:00 UTC the day before: shown in the clock's zone
	} {
		if got := whenText(c.at, now); got != c.want {
			t.Errorf("whenText(%s) = %q, want %q", c.at, got, c.want)
		}
	}
}

// ---- the run: an unfinished update is finished, with or without commits ----

func upToDate(h *harness) {
	h.mainCheck = "Already up to date."
	// what `hermes update` prints when no commit is waiting: no pull, no
	// "Update complete!", the checkout and the gateway stay on the same code
	h.onUpdate(0, "→ Update channel: main\n  Installing Python dependencies…\nBuilding desktop packaged app…\nPackaging the desktop app…\n✓ Already up to date!", false)
}

func finishHarness(t *testing.T) *harness {
	t.Helper()
	h := newPreflightHarness(t)
	upToDate(h)
	h.receipts = &fakeReceipts{prev: receiptDied()}
	return h
}

func updateArgv(h *harness) string { return strings.Join(h.updateCmd.Argv[1:], " ") }

func TestUpToDateButUnfinishedGoesOnAndSaysSo(t *testing.T) {
	h := finishHarness(t)
	h.con.Keys = []platform.Key{key('y')}
	var run *Run
	h.onRun = func(r *Run) { run = r }
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	h.mustContain(
		"last update", "did not finish ("+crashedWords+")",
		"No new commits, but the last update did not finish; going on to finish it.",
		"The last update did not finish ("+crashedWords+"). Hermes will close to finish it; that takes about",
		"Press Y to finish it now, any other key to abort (auto-abort in 2s",
		"Unfinished update finished and verified.", "RESULT: OK",
		"unchanged; no new commits")
	if strings.Contains(h.output(), "Hermes is up to date") {
		t.Errorf("an unfinished update must not end as \"up to date\":\n%s", h.output())
	}
	if h.updates != 1 || updateArgv(h) != "update --yes --keep-stash --branch main" {
		t.Errorf("updates=%d argv=%q (same argv as a normal run)", h.updates, updateArgv(h))
	}
	if calls := h.app.Calls(); len(calls) < 2 || calls[0] != "close 100" || calls[len(calls)-1] != "launch "+tDesktop {
		t.Errorf("app calls = %v", calls)
	}
	// verify accepts the unchanged HEAD: no false PROBLEM
	f := run.Facts
	if !f.Verified || f.HeadAfter != oldSHA || f.HeadAfter != f.HeadFull || f.Again != AgainFinish || !f.NothingNew {
		t.Errorf("facts: verified=%v head=%s/%s again=%q nothingNew=%v", f.Verified, f.HeadAfter, f.HeadFull, f.Again, f.NothingNew)
	}
}

// The footer must not wait for steps that cannot happen, and the history
// must not teach a later update that fetch and pull take no time.
func TestUnfinishedRunWithoutCommitsFinishesTheProgressAndTheRecord(t *testing.T) {
	h := finishHarness(t)
	h.con.Keys = []platform.Key{key('y')}
	var run *Run
	h.onRun = func(r *Run) { run = r }
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	done, ok := run.Est.Result()
	if !done || ok == nil || !*ok || run.Est.Fraction() != 1 {
		t.Errorf("progress did not finish: done=%v ok=%v fraction=%v", done, ok, run.Est.Fraction())
	}
	for _, k := range []string{"fetch", "pull", "finalize", "restart"} {
		if s := run.Est.State(k); s != timings.Skipped {
			t.Errorf("step %s is %v, want skipped (it cannot run without commits)", k, s)
		}
	}
	hist, err := timings.LoadHistory(h.timings)
	if err != nil || len(hist) != 1 {
		t.Fatalf("history %+v, %v", hist, err)
	}
	if rec := hist[0]; rec.Commits == nil || *rec.Commits != 0 || rec.OK == nil || !*rec.OK {
		t.Errorf("record = %+v, want an OK run that says it had 0 commits", rec)
	}
}

// plannedMinutes is the planned time of the prompt, from the seeds, with and
// without the steps that cannot run when no commit is waiting.
func plannedMinutes(t *testing.T, skip bool) int {
	t.Helper()
	cfg := newHarness(t).cfg
	e := timings.NewEstimator(timings.KindUpdate, timings.UpdateSteps(cfg.Tunables.StepSeconds), nil, cfg.Tunables, nil)
	if skip {
		for _, k := range noCommitSkips {
			e.ExpectSkip(k, true)
		}
	}
	return minutes(e.PlannedTotal("close"))
}

func TestUnfinishedPromptEstimateLeavesOutFetchAndPull(t *testing.T) {
	with, without := plannedMinutes(t, true), plannedMinutes(t, false)
	if with >= without {
		t.Fatalf("planned %d min with the skips and %d without: the test cannot tell them apart", with, without)
	}
	h := finishHarness(t)
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	h.mustContain("that takes about " + strconv.Itoa(with) + " min")
	if strings.Contains(h.output(), "that takes about "+strconv.Itoa(without)+" min") {
		t.Errorf("the prompt counts a fetch and a pull that cannot happen:\n%s", h.output())
	}
}

func TestUnfinishedCheckEstimateLeavesOutFetchAndPull(t *testing.T) {
	with := plannedMinutes(t, true)
	h := finishHarness(t)
	h.mode = ModeCheck
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	h.mustContain("about " + strconv.Itoa(with) + " min once Hermes closes")
}

func TestUpToDateAndUnfinishedByTheOwnLogAlone(t *testing.T) {
	h := finishHarness(t)
	h.receipts = nil
	h.logTail = func(string, int64) ([]byte, error) {
		return []byte(crashedRun(crashedAt.Add(-3*time.Minute), "→ Packaging the desktop app…") + ownStart(t0.Add(40*time.Hour))), nil
	}
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	h.mustContain("did not finish (stopped during packaging the desktop app at yesterday 8:5", "Unfinished update finished and verified.")
	if h.updates != 1 {
		t.Errorf("updates = %d", h.updates)
	}
}

func TestUpToDateAndUnfinishedByAStaleMarkerAlone(t *testing.T) {
	h := finishHarness(t)
	h.receipts = nil
	h.marker.stale = true
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	h.mustContain("last update", "did not finish (an update marker was left behind)",
		"The last update did not finish. Hermes will close to finish it", "Unfinished update finished and verified.")
	if h.updates != 1 {
		t.Errorf("updates = %d", h.updates)
	}
}

func TestPreviousUpdateIsLoggedOnceWithSignalTimeAndStage(t *testing.T) {
	h := finishHarness(t)
	var buf strings.Builder
	h.logSink = &buf
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	const want = `msg="previous update did not finish" signals=receipt-running at=2026-09-30T20:56:10Z stage="building the apps"`
	if n := strings.Count(buf.String(), want); n != 1 {
		t.Errorf("structured line found %d times, want once:\n%s", n, buf.String())
	}
}

// Anything that cannot be read is unknown: the run is exactly the run of today.
func TestUnreadableSignalsChangeNothing(t *testing.T) {
	h := newPreflightHarness(t)
	upToDate(h)
	h.receipts = &fakeReceipts{err: errors.New("receipt is not readable: invalid character")}
	h.logTail = func(string, int64) ([]byte, error) { return nil, errors.New("sharing violation") }
	var buf strings.Builder
	h.logSink = &buf
	if err := h.run(); err != nil {
		t.Fatalf("err = %v", err)
	}
	h.mustContain("Hermes is up to date")
	if h.updates != 0 || len(h.app.Calls()) != 0 || strings.Contains(h.output(), "last update") {
		t.Errorf("an unreadable signal changed the run: updates=%d app=%v\n%s", h.updates, h.app.Calls(), h.output())
	}
	for _, want := range []string{"receipt not readable; unknown", "own log not usable; unknown"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("the log does not say %q:\n%s", want, buf.String())
		}
	}
}

func TestFinishedPreviousUpdateIsNotMentioned(t *testing.T) {
	h := newPreflightHarness(t)
	upToDate(h)
	h.receipts = &fakeReceipts{}
	h.logTail = func(string, int64) ([]byte, error) {
		return []byte(finishedRun(t0, pidOld) + ownStart(t0.Add(time.Hour))), nil
	}
	if err := h.run(); err != nil || h.updates != 0 {
		t.Fatalf("err=%v updates=%d", err, h.updates)
	}
	h.mustContain("Hermes is up to date")
	if strings.Contains(h.output(), "did not finish") {
		t.Errorf("output mentions an unfinished update:\n%s", h.output())
	}
}

func TestNewCommitsAndUnfinishedAddsOneSentenceToThePrompt(t *testing.T) {
	h := newPreflightHarness(t)
	h.receipts = &fakeReceipts{prev: receiptDied()}
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	h.mustContain("Hermes will close for the update. This update also finishes the previous update, which did not finish ("+crashedWords+"). The update takes about",
		"Press Y to continue now, any other key to abort",
		"Hermes is updated and verified.", "v1.0 → v1.1")
	if strings.Contains(h.output(), "Unfinished update finished") || strings.Contains(h.output(), "No new commits") {
		t.Errorf("a run with new commits is a normal update:\n%s", h.output())
	}
	if h.updates != 1 || updateArgv(h) != "update --yes --keep-stash --branch main" {
		t.Errorf("updates=%d argv=%q", h.updates, updateArgv(h))
	}
}

func TestPromptIsTheUsualOneWhenTheLastUpdateFinished(t *testing.T) {
	h := newPreflightHarness(t)
	h.receipts = &fakeReceipts{}
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	h.mustContain("Hermes will close for the update. The update takes about")
	if strings.Contains(h.output(), "did not finish") {
		t.Errorf("an extra sentence for a finished update:\n%s", h.output())
	}
}

func TestUnfinishedIsFinishedWithoutAskingWhenUnattendedOrYes(t *testing.T) {
	for _, flag := range []string{"unattended", "yes"} {
		t.Run(flag, func(t *testing.T) {
			h := finishHarness(t) // no key scripted: a prompt would end in a timeout and a cancel
			if err := h.run(flag); err != nil {
				t.Fatalf("err = %v\n%s", err, h.output())
			}
			if h.updates != 1 || strings.Contains(h.output(), "Press Y") {
				t.Errorf("updates=%d, a question was asked:\n%s", h.updates, h.output())
			}
			h.mustContain("Unfinished update finished and verified.")
		})
	}
}

func TestUnfinishedInCheckModeShowsTheRowAndClosesNothing(t *testing.T) {
	h := finishHarness(t)
	h.mode = ModeCheck
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	h.mustContain("last update", "did not finish ("+crashedWords+"); run Hermes Safe Update to finish it",
		"The last update did not finish. Hermes was not touched.", "no new commits (the last update still needs finishing)")
	if h.updates != 0 || len(h.app.Calls()) != 0 || h.marker.claims != 0 || len(h.procs.Killed()) != 0 || h.gw.stops != 0 {
		t.Errorf("check mode changed something: updates=%d app=%v marker=%+v", h.updates, h.app.Calls(), h.marker)
	}
	if strings.Contains(h.output(), "Hermes is up to date") || strings.Contains(h.output(), "Press") {
		t.Errorf("check mode asked or ended as up to date:\n%s", h.output())
	}
}

func TestUnfinishedRunThatFailsKeepsTheProblemWording(t *testing.T) {
	h := finishHarness(t)
	h.verifier = fakeVerifier{ok: false, out: "desktop build is older than the checkout"}
	h.con.Keys = []platform.Key{key('y')}
	err := h.run()
	if !errors.Is(err, apperr.ErrUpdateProblem) {
		t.Fatalf("err = %v, want the update problem\n%s", err, h.output())
	}
	h.mustContain("The update needs attention: see RESULT above.", "RESULT: PROBLEM")
	if strings.Contains(h.output(), "Unfinished update finished") {
		t.Errorf("a failed run says it finished:\n%s", h.output())
	}
}

// ---- the repair offer on the up-to-date screen ----

func offerHarness(t *testing.T) *harness {
	t.Helper()
	h := newPreflightHarness(t)
	upToDate(h)
	h.con.Terminal = true
	return h
}

func TestRepairOfferedWhenUpToDateAndRRepairsWithoutASecondQuestion(t *testing.T) {
	h := offerHarness(t)
	h.con.Keys = []platform.Key{key('r')} // one key only: a second prompt would time out and cancel
	var run *Run
	h.onRun = func(r *Run) { run = r }
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	h.mustContain("Hermes is up to date. Press R to repair this install: Hermes closes and the update steps run again (dependencies, builds, gateway restart), about",
		"Any other key ends the run without repairing (it ends by itself in 2s",
		"Repair finished and verified.", "unchanged; no new commits")
	if strings.Contains(h.output(), "Press Y") {
		t.Errorf("R already said yes, Y was asked again:\n%s", h.output())
	}
	if h.updates != 1 || updateArgv(h) != "update --yes --keep-stash --branch main" || len(h.con.Keys) != 0 {
		t.Errorf("updates=%d argv=%q keys left=%d", h.updates, updateArgv(h), len(h.con.Keys))
	}
	if f := run.Facts; f.Again != AgainRepair || !f.Confirmed || !f.Verified {
		t.Errorf("facts: again=%q confirmed=%v verified=%v", f.Again, f.Confirmed, f.Verified)
	}
	if calls := h.app.Calls(); len(calls) < 2 || calls[0] != "close 100" {
		t.Errorf("app calls = %v", calls)
	}
}

// Every other answer leaves the up-to-date result alone: exit 0, the card,
// nothing closed.
func TestRepairOfferOtherKeysAndTimeoutExitWithTheUpToDateCard(t *testing.T) {
	for name, k := range map[string]platform.Key{
		"enter": key('\r'), "escape": key(0x1b), "letter": key('x'), "n": key('n'), "y": key('y'),
		"space": key(' '), "digit": key('1'), "ctrl+c": {CtrlC: true},
	} {
		t.Run(name, func(t *testing.T) {
			h := offerHarness(t)
			h.con.Keys = []platform.Key{k}
			if err := h.run(); err != nil {
				t.Fatalf("err = %v", err)
			}
			h.mustContain("Press R to repair this install", "Hermes is up to date")
			if h.updates != 0 || len(h.app.Calls()) != 0 || h.marker.claims != 0 || h.gw.stops != 0 {
				t.Errorf("something changed: updates=%d app=%v", h.updates, h.app.Calls())
			}
			if strings.Contains(h.output(), "Repair finished") || strings.Contains(h.output(), "Press Y") {
				t.Errorf("went on:\n%s", h.output())
			}
		})
	}
	t.Run("timeout", func(t *testing.T) {
		h := offerHarness(t)
		scripted(h, scriptStep{timeout: true, after: 10 * time.Minute})
		if err := h.run(); err != nil {
			t.Fatalf("err = %v", err)
		}
		h.mustContain("Press R to repair this install", "Hermes is up to date")
		if h.updates != 0 || len(h.app.Calls()) != 0 {
			t.Errorf("a timeout repaired: updates=%d app=%v", h.updates, h.app.Calls())
		}
	})
}

func TestRepairIsNotOfferedWhereNobodyCanAnswer(t *testing.T) {
	cases := map[string]func(h *harness) []string{
		"unattended":  func(h *harness) []string { return []string{"unattended"} },
		"yes":         func(h *harness) []string { return []string{"yes"} },
		"no terminal": func(h *harness) []string { h.con.Terminal = false; return nil },
		"check":       func(h *harness) []string { h.mode = ModeCheck; return nil },
		"check + yes": func(h *harness) []string { h.mode = ModeCheck; return []string{"yes"} },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			h := offerHarness(t)
			flags := setup(h)
			if err := h.run(flags...); err != nil {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(h.output(), "Press R") {
				t.Errorf("the repair was offered:\n%s", h.output())
			}
			h.mustContain("Hermes is up to date")
			if h.updates != 0 || len(h.app.Calls()) != 0 {
				t.Errorf("changed something: updates=%d", h.updates)
			}
		})
	}
	// --force goes on anyway: no question about it either
	h := offerHarness(t)
	if err := h.run("force", "yes"); err != nil || h.updates != 1 || strings.Contains(h.output(), "Press R") {
		t.Errorf("--force: err=%v updates=%d\n%s", err, h.updates, h.output())
	}
	// ... and an unfinished update is finished instead of offering a repair
	h = offerHarness(t)
	h.receipts = &fakeReceipts{prev: receiptDied()}
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil || h.updates != 1 || strings.Contains(h.output(), "Press R") {
		t.Errorf("unfinished: err=%v updates=%d\n%s", err, h.updates, h.output())
	}
}

// Every safety step still applies after an R.
func TestRepairAfterRStillWaitsForBusySessions(t *testing.T) {
	h := offerHarness(t)
	h.sessions.script = [][]hermes.Session{busy("s1")}
	h.con.Keys = []platform.Key{key('r'), key('a')} // R, then A at the busy-session question
	err := h.run()
	if !errors.Is(err, apperr.ErrCancelled) || h.updates != 0 || len(h.app.Calls()) != 0 {
		t.Fatalf("err=%v updates=%d app=%v\n%s", err, h.updates, h.app.Calls(), h.output())
	}
	h.mustContain("1 active in the last", "aborted by user")
}

func TestRepairAfterRRechecksTheCheckoutLockBeforeClosing(t *testing.T) {
	h := offerHarness(t)
	h.procs.Add(platform.Process{PID: 4242, PPID: 1, Name: "python.exe"})
	// an update starts while the window waits
	scripted(h, scriptStep{key: 'r', after: 5 * time.Minute, act: func() { holdLock(h, lockRecordFixture) }})
	err := h.run()
	if !errors.Is(err, apperr.ErrPreflightStop) || h.updates != 0 || len(h.app.Calls()) != 0 || h.marker.claims != 0 {
		t.Fatalf("err=%v updates=%d app=%v\n%s", err, h.updates, h.app.Calls(), h.output())
	}
	h.mustContain("An update is running (pid 4242", "Nothing was closed")
}

// The repair command is a run like any other: it stops inside a chat.
func TestRepairCommandStopsInsideAHermesChatLikeAnyRun(t *testing.T) {
	h := offerHarness(t)
	h.repair = true
	h.env = map[string]string{"HERMES_SESSION_ID": "x"}
	h.con.Keys = []platform.Key{key('y')}
	err := h.run()
	if !errors.Is(err, apperr.ErrPreflightStop) || h.updates != 0 || len(h.app.Calls()) != 0 {
		t.Fatalf("err=%v updates=%d", err, h.updates)
	}
}

// The seconds the person spent reading the offer are not the check's work.
func TestRepairWindowTimeIsNotRecordedAsTheChecksTime(t *testing.T) {
	h := offerHarness(t)
	scripted(h, scriptStep{key: 'x', after: 7 * time.Minute})
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	hist, err := timings.LoadHistory(h.timings)
	if err != nil || len(hist) != 1 {
		t.Fatalf("history %+v, %v", hist, err)
	}
	if v, _ := hist[0].Steps.Get("remote"); v > 60 {
		t.Errorf("the remote step is recorded with %.0f s: the answering time was counted", v)
	}
}

// ---- the repair command ----

func TestRepairCommandGoesAheadWithoutCommitsAndAsksY(t *testing.T) {
	h := newPreflightHarness(t)
	upToDate(h)
	h.repair = true
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	h.mustContain("No new commits; repairing: the update steps run again.",
		"Hermes will close to repair this install: the update steps run again (dependencies, builds, gateway restart); that takes about",
		"Press Y to repair it now, any other key to abort", "Repair finished and verified.")
	if strings.Contains(h.output(), "Press R") || h.updates != 1 {
		t.Errorf("updates=%d:\n%s", h.updates, h.output())
	}
}

func TestRepairCommandNeedsTheYAndAbortsOnAnyOtherKey(t *testing.T) {
	h := newPreflightHarness(t)
	upToDate(h)
	h.repair = true
	h.con.Keys = []platform.Key{key('n')}
	if err := h.run(); !errors.Is(err, apperr.ErrCancelled) || h.updates != 0 || len(h.app.Calls()) != 0 {
		t.Fatalf("err=%v updates=%d", err, h.updates)
	}
}

func TestRepairCommandUnattendedGoesWithoutAsking(t *testing.T) {
	h := newPreflightHarness(t)
	upToDate(h)
	h.repair = true
	if err := h.run("unattended"); err != nil || h.updates != 1 {
		t.Fatalf("err=%v updates=%d\n%s", err, h.updates, h.output())
	}
}

// With new commits `repair` is a normal update.
func TestRepairCommandWithNewCommitsIsANormalUpdate(t *testing.T) {
	h := newPreflightHarness(t)
	h.repair = true
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	h.mustContain("Hermes will close for the update. The update takes about", "Hermes is updated and verified.", "v1.0 → v1.1")
	if strings.Contains(h.output(), "repair") {
		t.Errorf("repair wording on an update with commits:\n%s", h.output())
	}
}

// --force keeps today's behaviour: runs although nothing is new, normal wording.
func TestForceKeepsItsOwnWording(t *testing.T) {
	h := newPreflightHarness(t)
	upToDate(h)
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run("force"); err != nil {
		t.Fatal(err)
	}
	h.mustContain("--force: running even though no update was found", "Hermes will close for the update. The update takes about", "Hermes is updated and verified.")
	if strings.Contains(h.output(), "Repair") || strings.Contains(h.output(), "Unfinished") {
		t.Errorf("force took the repair wording:\n%s", h.output())
	}
}

// ---- the dependency rows after an update that did not finish ----

const owedPy = "unchanged in the new commits; the last update did not finish, so Hermes may still reinstall the Python packages"
const owedNode = "unchanged in the new commits; the last update did not finish, so Hermes may still rebuild the Node packages"

func depsRowsHarness(t *testing.T, unfinished bool, py, npm *bool) *harness {
	t.Helper()
	h := newPreflightHarness(t)
	if unfinished {
		h.receipts = &fakeReceipts{prev: receiptDied()}
	}
	h.repo.diffPy, h.repo.diffNPM = py, npm
	h.mode = ModeCheck
	return h
}

func TestDepsRowsDoNotClaimUnchangedAfterAnUnfinishedUpdate(t *testing.T) {
	no := false
	h := depsRowsHarness(t, true, &no, &no)
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	// the pre-flight rows and the check summary
	for _, want := range []string{"python deps", owedPy, "node packages", owedNode, "Python deps", "Node packages"} {
		h.mustContain(want)
	}
	if n := strings.Count(h.output(), owedPy); n != 2 {
		t.Errorf("owed python line appears %d times, want 2 (pre-flight row and check summary):\n%s", n, h.output())
	}
	if regexp.MustCompile(`(?mi)^ *(python deps|node packages):? +unchanged *$`).MatchString(h.output()) {
		t.Errorf("a bare unchanged survived:\n%s", h.output())
	}
}

func TestDepsRowsKeepTheirWordsWhenTheLastUpdateFinished(t *testing.T) {
	no := false
	h := depsRowsHarness(t, false, &no, &no)
	h.receipts = &fakeReceipts{}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.output(), "did not finish") {
		t.Errorf("unfinished wording on a clean install:\n%s", h.output())
	}
	h.mustContain("unchanged")
}

func TestDepsRowsAfterAnUnfinishedUpdateKeepChangedAndUnknown(t *testing.T) {
	yes := true
	h := depsRowsHarness(t, true, &yes, nil)
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	h.mustContain("changed, will be reinstalled")
	if strings.Contains(h.output(), "may still") {
		t.Errorf("a changed or unknown dependency row got the owed wording:\n%s", h.output())
	}
}

func TestDepsRowsAreQualifiedByAStaleMarkerAlone(t *testing.T) {
	no := false
	h := depsRowsHarness(t, false, &no, &no)
	h.receipts = &fakeReceipts{}
	h.marker.stale = true
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	h.mustContain(owedPy)
}

// The release-channel rows qualify "unchanged" too, not only the main ones.
func TestDepsRowsAfterAnUnfinishedUpdateOnAReleaseChannel(t *testing.T) {
	no := false
	h, _ := releaseHarnessWith(t, func(h *harness, m *fakeMirror) {
		h.repo.relRef, h.repo.relSHA = "refs/tags/v0.22.1", relSHA
		h.repo.diffPy, h.repo.diffNPM = &no, &no
		h.receipts = &fakeReceipts{prev: receiptDied()}
	})
	h.mustContain("release v0.22.1 available")
	// the pre-flight row and the check summary, for each of the two
	for _, want := range []string{owedPy, owedNode} {
		if n := strings.Count(h.output(), want); n != 2 {
			t.Errorf("%q appears %d times, want 2:\n%s", want, n, h.output())
		}
	}
}
