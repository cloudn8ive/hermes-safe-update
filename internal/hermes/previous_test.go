package hermes

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// receiptFixture reads a synthetic receipt of testdata/contracts.
func receiptFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "contracts", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// receiptHome lays a latest.json under a temp Hermes home.
func receiptHome(t *testing.T, body []byte) string {
	t.Helper()
	home := t.TempDir()
	if body != nil {
		p := filepath.Join(append([]string{home}, append(append([]string(nil), ReceiptDirParts...), ReceiptLatestFile)...)...)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func reader(home string, procs ...platform.Process) ReceiptReader {
	return ReceiptReader{Home: home, Procs: platform.NewFakeProcs(procs...)}
}

// The incident: the run was killed while building; its receipt stayed
// "running" and its owner is gone.
func TestReceiptRunningWithADeadOwnerIsUnfinished(t *testing.T) {
	p, err := reader(receiptHome(t, receiptFixture(t, "update-receipt-running.json"))).Unfinished()
	if err != nil || p == nil {
		t.Fatalf("Unfinished() = %v, %v", p, err)
	}
	if len(p.Signals) != 1 || p.Signals[0] != SignalReceiptRunning {
		t.Errorf("signals = %v", p.Signals)
	}
	if p.StepKey != "build" {
		t.Errorf("step = %q (the last recorded stage is deps: it stopped in the builds)", p.StepKey)
	}
	if len(p.Keys) != 1 || p.Keys[0] != "receipt:0f1e2d3c4b5a69780f1e2d3c4b5a6978" {
		t.Errorf("keys = %v, want the update id", p.Keys)
	}
	want := time.Date(2026, 9, 27, 20, 56, 10, 0, time.UTC)
	if !p.At.Equal(want) {
		t.Errorf("at = %s, want the last stage's time %s", p.At, want)
	}
}

// An update that is running right now is not an unfinished one: the marker
// and lock checks deal with it.
func TestReceiptRunningWithALiveOwnerIsNotUnfinished(t *testing.T) {
	for name, procs := range map[string][]platform.Process{
		"the run's pid lives":   {{PID: 7001}},
		"only the writer lives": {{PID: 7002}},
		"both live":             {{PID: 7001}, {PID: 7002}},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := reader(receiptHome(t, receiptFixture(t, "update-receipt-running.json")), procs...).Unfinished()
			if err != nil || p != nil {
				t.Errorf("Unfinished() = %v, %v; an update that is running is not unfinished", p, err)
			}
		})
	}
}

// A reused pid is not the owner: the creation time recorded in the receipt
// decides, with Hermes' 2 s tolerance.
func TestReceiptOwnerIsTheSameIncarnationOnly(t *testing.T) {
	home := receiptHome(t, receiptFixture(t, "update-receipt-running.json"))
	owner := platform.Process{PID: 7001}
	mk := func(sec float64) ReceiptReader {
		r := reader(home, owner)
		r.Created = created(map[int]time.Time{7001: epochTime(sec)})
		return r
	}
	if p, err := mk(1790000000.5).Unfinished(); err != nil || p != nil {
		t.Errorf("same incarnation: %v, %v", p, err)
	}
	if p, err := mk(1790000001.9).Unfinished(); err != nil || p != nil {
		t.Errorf("within the tolerance: %v, %v", p, err)
	}
	if p, err := mk(1790099999).Unfinished(); err != nil || p == nil {
		t.Errorf("a process that started later reuses the pid: %v, %v", p, err)
	}
	// a creation time that cannot be read proves nothing against a live pid
	r := reader(home, owner)
	r.Created = func(int) (time.Time, bool) { return time.Time{}, false }
	if p, err := r.Unfinished(); err != nil || p != nil {
		t.Errorf("unreadable creation time: %v, %v", p, err)
	}
}

func TestReceiptInterruptedIsUnfinished(t *testing.T) {
	// the owner's pid being alive does not matter: Hermes already decided
	p, err := reader(receiptHome(t, receiptFixture(t, "update-receipt-interrupted.json")), platform.Process{PID: 7001}).Unfinished()
	if err != nil || p == nil {
		t.Fatalf("Unfinished() = %v, %v", p, err)
	}
	if p.Signals[0] != SignalReceiptInterrupted || p.StepKey != "build" {
		t.Errorf("got %+v", p)
	}
	if !p.At.Equal(time.Date(2026, 9, 27, 20, 56, 10, 0, time.UTC)) {
		t.Errorf("at = %s", p.At)
	}
}

// The end the receipt recorded wins over its stages (an update stopped with
// Ctrl+C writes "interrupted" with a finished_at).
func TestReceiptFinishedAtBeatsTheLastStage(t *testing.T) {
	body := `{"outcome":"interrupted","started_at":"2026-09-27T20:00:00+00:00","finished_at":"2026-09-27T20:09:00+00:00","stages":[{"name":"apply","at":"2026-09-27T20:05:00+00:00"}]}`
	p, err := reader(receiptHome(t, []byte(body))).Unfinished()
	if err != nil || p == nil {
		t.Fatalf("%v, %v", p, err)
	}
	if !p.At.Equal(time.Date(2026, 9, 27, 20, 9, 0, 0, time.UTC)) || p.StepKey != "pydeps" {
		t.Errorf("got %+v", p)
	}
}

func TestReceiptWithoutStagesFallsBackToTheStart(t *testing.T) {
	body := `{"outcome":"running","started_at":"2026-09-27T20:00:00+00:00"}`
	p, err := reader(receiptHome(t, []byte(body))).Unfinished()
	if err != nil || p == nil {
		t.Fatalf("%v, %v", p, err)
	}
	if !p.At.Equal(time.Date(2026, 9, 27, 20, 0, 0, 0, time.UTC)) || p.StepKey != "start" || p.Keys[0] != "receipt:2026-09-27T20:00:00+00:00" {
		t.Errorf("a receipt with no pid and no stage is a dead run that never got going: %+v", p)
	}
}

func TestReceiptOfAFinishedRunIsNotUnfinished(t *testing.T) {
	for _, outcome := range []string{"success", "failed", "partial", "refused", ""} {
		body := `{"outcome":"` + outcome + `","pid":7001,"stages":[{"name":"plan"}]}`
		if p, err := reader(receiptHome(t, []byte(body))).Unfinished(); err != nil || p != nil {
			t.Errorf("outcome %q: %v, %v", outcome, p, err)
		}
	}
	if p, err := reader(receiptHome(t, receiptFixture(t, "update-receipt-success.json"))).Unfinished(); err != nil || p != nil {
		t.Errorf("the success fixture: %v, %v", p, err)
	}
}

// Unknown changes nothing: the caller gets an error to log, never a verdict.
func TestReceiptThatCannotBeReadIsUnknown(t *testing.T) {
	for name, body := range map[string]string{
		"garbage":        "this is not json",
		"empty":          "",
		"json array":     "[1,2,3]",
		"wrong type":     `{"outcome":"running","pid":"seven"}`,
		"truncated json": `{"outcome":"running","pid":70`,
	} {
		t.Run(name, func(t *testing.T) {
			p, err := reader(receiptHome(t, []byte(body))).Unfinished()
			if p != nil || err == nil {
				t.Errorf("got %v, %v; garbage must be an error and no verdict", p, err)
			}
		})
	}
	// a read error other than "missing" is unknown too
	r := reader(receiptHome(t, nil))
	r.ReadFile = func(string) ([]byte, error) { return nil, errors.New("access denied") }
	if p, err := r.Unfinished(); p != nil || err == nil {
		t.Errorf("read error: %v, %v", p, err)
	}
	// "running" without a way to look at processes cannot be judged
	r = ReceiptReader{Home: receiptHome(t, receiptFixture(t, "update-receipt-running.json"))}
	if p, err := r.Unfinished(); p != nil || err == nil {
		t.Errorf("no process lookup: %v, %v", p, err)
	}
}

// A Hermes without receipts has no folder: the signal is absent, not an error.
func TestOldHermesWithoutReceiptsIsNotAnError(t *testing.T) {
	if p, err := reader(receiptHome(t, nil)).Unfinished(); p != nil || err != nil {
		t.Errorf("no folder: %v, %v", p, err)
	}
	r := reader(t.TempDir())
	r.ReadFile = func(string) ([]byte, error) { return nil, &fs.PathError{Op: "open", Path: "x", Err: fs.ErrNotExist} }
	if p, err := r.Unfinished(); p != nil || err != nil {
		t.Errorf("missing file: %v, %v", p, err)
	}
	if p, err := (ReceiptReader{}).Unfinished(); p != nil || err != nil {
		t.Errorf("no home: %v, %v", p, err)
	}
}

func TestReceiptBOMAndPathAreHermes(t *testing.T) {
	body := append([]byte("\ufeff"), receiptFixture(t, "update-receipt-interrupted.json")...)
	home := receiptHome(t, body)
	r := reader(home)
	if p, err := r.Unfinished(); err != nil || p == nil {
		t.Errorf("BOM: %v, %v", p, err)
	}
	want := filepath.Join(home, "logs", "update_receipts", "latest.json")
	if r.Path() != want {
		t.Errorf("path = %s, want %s", r.Path(), want)
	}
}

func TestReceiptStopStepNames(t *testing.T) {
	for stage, want := range map[string]string{
		"": "start", "plan": "pull", "snapshot": "pull",
		"apply": "pydeps", "deps": "build", "build": "restart", "restart": "tail", "from-the-future": "",
	} {
		if got := ReceiptStopStep(stage); got != want {
			t.Errorf("ReceiptStopStep(%q) = %q, want %q", stage, got, want)
		}
	}
	if ReceiptStopStep("deps") == "pydeps" {
		t.Error("deps was recorded when it ENDED: the run stopped in the step after it")
	}
}

// ---- the folder of run files (latest.json is not the newest update) ----

// runFile lays update_<name>.json in the receipt folder with the given file time.
func runFile(t *testing.T, home, name string, body []byte, mod time.Time) {
	t.Helper()
	dir := filepath.Join(append([]string{home}, ReceiptDirParts...)...)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
}

var runAt = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

// The incident of the review: the update was killed, Hermes started later and
// its plugin check replaced latest.json. The crashed run's own file still says
// "running", and that is what must be seen.
func TestCrashedUpdateIsSeenAfterAPluginCheckReplacedLatest(t *testing.T) {
	home := receiptHome(t, receiptFixture(t, "pm-receipt-plugin-check.json")) // latest.json = a plugin check
	runFile(t, home, "update_20260927_205505_7001_0f1e2d3c.json", receiptFixture(t, "update-receipt-running.json"), runAt)
	runFile(t, home, "pm_20260928T091106Z-plugin-check-7003-aaaa.json", receiptFixture(t, "pm-receipt-plugin-check.json"), runAt.Add(time.Hour))
	p, err := reader(home).Unfinished()
	if err != nil || p == nil || !p.Has(SignalReceiptRunning) {
		t.Fatalf("Unfinished() = %v, %v; the update_*.json file says running with a dead owner", p, err)
	}
}

// A newer update that finished clears an older one that did not.
func TestNewerFinishedUpdateClearsAnOlderRunningOne(t *testing.T) {
	home := receiptHome(t, nil)
	runFile(t, home, "update_20260927_205505_7001_0f1e2d3c.json", receiptFixture(t, "update-receipt-running.json"), runAt)                // started 09-27
	runFile(t, home, "update_20260928_103255_7003_1f1e2d3c.json", receiptFixture(t, "update-receipt-success.json"), runAt.Add(time.Hour)) // started 09-28
	if p, err := reader(home).Unfinished(); err != nil || p != nil {
		t.Errorf("Unfinished() = %v, %v; the newest update finished", p, err)
	}
	// ... and the other way round: the newest one is the unfinished one
	home = receiptHome(t, nil)
	later := strings.Replace(string(receiptFixture(t, "update-receipt-interrupted.json")), "2026-09-27T20:55:05", "2026-10-01T20:55:05", 1)
	runFile(t, home, "update_20260928_103255_7003_1f1e2d3c.json", receiptFixture(t, "update-receipt-success.json"), runAt)
	runFile(t, home, "update_20261001_205505_7001_0f1e2d3c.json", []byte(later), runAt.Add(time.Hour))
	if p, err := reader(home).Unfinished(); err != nil || p == nil {
		t.Errorf("the newest is interrupted: %v, %v", p, err)
	}
}

// The newest by start time wins, not the last written file: reconcile
// rewrites an old record when it marks it interrupted.
func TestNewestRunIsTheOneThatStartedLast(t *testing.T) {
	home := receiptHome(t, nil)
	runFile(t, home, "update_20260928_103255_7003_1f1e2d3c.json", receiptFixture(t, "update-receipt-success.json"), runAt)                    // started 09-28
	runFile(t, home, "update_20260927_205505_7001_0f1e2d3c.json", receiptFixture(t, "update-receipt-interrupted.json"), runAt.Add(time.Hour)) // started 09-27, written last
	if p, err := reader(home).Unfinished(); err != nil || p != nil {
		t.Errorf("Unfinished() = %v, %v; the success started after the interrupted run", p, err)
	}
}

func TestPluginManagerReceiptsAreNotUpdates(t *testing.T) {
	home := receiptHome(t, receiptFixture(t, "pm-receipt-plugin-check.json"))
	runFile(t, home, "pm_20260928T091106Z-plugin-check-7003-aaaa.json", []byte(`{"outcome":"running","pid":7001,"started_at":"2027-01-01T00:00:00+00:00"}`), runAt)
	// no update_*.json: the fallback is latest.json (a plugin check: not unfinished)
	if p, err := reader(home).Unfinished(); err != nil || p != nil {
		t.Errorf("Unfinished() = %v, %v; a pm_ file must not count as an update", p, err)
	}
}

func TestOnlyTheLastTwentyRunFilesAreRead(t *testing.T) {
	home := receiptHome(t, nil)
	for i := 0; i < ReceiptKeep+5; i++ {
		runFile(t, home, fmt.Sprintf("update_%02d.json", i), []byte(`{"outcome":"success","started_at":"2026-09-01T00:00:00+00:00"}`), runAt.Add(time.Duration(i)*time.Minute))
	}
	var read []string
	r := reader(home)
	r.ReadFile = func(p string) ([]byte, error) { read = append(read, filepath.Base(p)); return readBounded(p) }
	if p, err := r.Unfinished(); err != nil || p != nil {
		t.Fatalf("Unfinished() = %v, %v", p, err)
	}
	if len(read) != ReceiptKeep || read[0] != "update_05.json" {
		t.Errorf("read %d files, first %v; want the %d newest by file time", len(read), read, ReceiptKeep)
	}
}

// One garbage run file does not hide the others; only garbage everywhere is unknown.
func TestUnreadableRunFileIsSkippedUnlessItIsAllThereIs(t *testing.T) {
	home := receiptHome(t, nil)
	runFile(t, home, "update_a.json", []byte("not json"), runAt.Add(time.Hour))
	runFile(t, home, "update_b.json", receiptFixture(t, "update-receipt-interrupted.json"), runAt)
	if p, err := reader(home).Unfinished(); err != nil || p == nil {
		t.Errorf("Unfinished() = %v, %v", p, err)
	}
	home = receiptHome(t, nil)
	runFile(t, home, "update_a.json", []byte("not json"), runAt)
	if p, err := reader(home).Unfinished(); err == nil || p != nil {
		t.Errorf("only garbage: %v, %v; that is unknown", p, err)
	}
}

// ---- the newest update that ran to its end ----

func TestNewestFinishedIsTheLatestStartOfAnUpdateThatEnded(t *testing.T) {
	home := receiptHome(t, nil)
	runFile(t, home, "update_20260927_205505_7001_0f1e2d3c.json", receiptFixture(t, "update-receipt-running.json"), runAt)                               // started 09-27, never ended
	runFile(t, home, "update_20260928_103255_7003_1f1e2d3c.json", receiptFixture(t, "update-receipt-success.json"), runAt.Add(time.Hour))                // started 09-28, success
	runFile(t, home, "pm_20260929T091106Z-plugin-check-7003-aaaa.json", []byte(`{"outcome":"success","started_at":"2026-10-05T00:00:00+00:00"}`), runAt) // not an update
	got, err := reader(home).NewestFinished()
	want := time.Date(2026, 9, 28, 10, 32, 55, 358465000, time.UTC)
	if err != nil || !got.Equal(want) {
		t.Errorf("NewestFinished() = %s, %v; want %s", got, err, want)
	}
}

func TestNewestFinishedCountsSuccessAndPartialOnly(t *testing.T) {
	for outcome, want := range map[string]bool{
		"success": true, "partial": true, "failed": false,
		"running": false, "interrupted": false, "refused": false, "": false,
	} {
		home := receiptHome(t, nil)
		runFile(t, home, "update_a.json", []byte(`{"outcome":"`+outcome+`","started_at":"2026-09-28T10:32:55+00:00"}`), runAt)
		got, err := reader(home).NewestFinished()
		if err != nil || got.IsZero() == want {
			t.Errorf("outcome %q: NewestFinished() = %s, %v; finished=%v wanted", outcome, got, err, want)
		}
	}
}

func TestNewestFinishedKnowsNothingWithoutReceipts(t *testing.T) {
	if got, err := (ReceiptReader{}).NewestFinished(); !got.IsZero() || err != nil {
		t.Errorf("no home: %s, %v", got, err)
	}
	if got, err := reader(receiptHome(t, nil)).NewestFinished(); !got.IsZero() || err != nil {
		t.Errorf("an older Hermes without receipts: %s, %v", got, err)
	}
	home := receiptHome(t, nil)
	runFile(t, home, "update_a.json", []byte("not json"), runAt)
	if got, err := reader(home).NewestFinished(); !got.IsZero() || err == nil {
		t.Errorf("garbage only: %s, %v; that is unknown (an error), never a finished update", got, err)
	}
	// latest.json alone: a plugin check is not an update that ended
	if got, err := reader(receiptHome(t, receiptFixture(t, "pm-receipt-plugin-check.json"))).NewestFinished(); !got.IsZero() || err != nil {
		t.Errorf("a plugin check in latest.json: %s, %v", got, err)
	}
	// ... and neither is a failed or bisected plugin-manager receipt there (pm outcomes: ok | refused | failed | bisected)
	for _, outcome := range []string{"ok", "failed", "bisected", "refused"} {
		pm := []byte(`{"outcome":"` + outcome + `","started_at":"2026-10-05T00:00:00+00:00"}`)
		if got, err := reader(receiptHome(t, pm)).NewestFinished(); !got.IsZero() || err != nil {
			t.Errorf("a %s plugin-manager receipt in latest.json: %s, %v", outcome, got, err)
		}
	}
}

// ---- a profile home reads the ROOT home's receipts ----

func TestRootHomeIsTheHomeAboveProfiles(t *testing.T) {
	root := filepath.Join("data", "hermes")
	for name, c := range map[string]struct{ home, want string }{
		"a profile":       {filepath.Join(root, "profiles", "work"), root},
		"the root itself": {root, root},
		"a folder called profiles with nothing above it": {filepath.Join("profiles", "x"), "."},
		"profiles at the end is not a profile":           {filepath.Join(root, "profiles"), filepath.Join(root, "profiles")},
		"deeper than a profile":                          {filepath.Join(root, "profiles", "work", "sub"), filepath.Join(root, "profiles", "work", "sub")},
		"empty":                                          {"", ""},
	} {
		if got := RootHome(c.home); got != c.want {
			t.Errorf("%s: RootHome(%q) = %q, want %q", name, c.home, got, c.want)
		}
	}
}

// Hermes writes update_*.json to the root home whichever profile it runs in:
// Safe Update pointed at a profile's home must look there.
func TestProfileHomeReadsTheRootHomesReceipts(t *testing.T) {
	root := t.TempDir()
	profile := filepath.Join(root, "profiles", "work")
	if err := os.MkdirAll(profile, 0o755); err != nil {
		t.Fatal(err)
	}
	runFile(t, root, "update_20260927_205505_7001_0f1e2d3c.json", receiptFixture(t, "update-receipt-running.json"), runAt)
	r := reader(profile)
	if want := filepath.Join(root, filepath.Join(ReceiptDirParts...)); r.Dir() != want {
		t.Errorf("Dir() = %q, want %q", r.Dir(), want)
	}
	p, err := r.Unfinished()
	if err != nil || p == nil || !p.Has(SignalReceiptRunning) {
		t.Errorf("Unfinished() = %v, %v; the crashed run's file is in the root home", p, err)
	}
	if got, err := reader(profile).NewestFinished(); !got.IsZero() || err != nil {
		t.Errorf("NewestFinished() = %s, %v", got, err)
	}
}
