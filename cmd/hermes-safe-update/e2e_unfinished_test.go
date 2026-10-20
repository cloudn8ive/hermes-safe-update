package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// End-to-end scenarios of v0.5.0 (see e2e_test.go for the harness): an update
// that did not finish is finished although no commit is waiting, the up-to-date
// screen offers a repair, and the `repair` command. Synthetic receipts and log
// lines only.

// writeReceipt lays Hermes' newest update receipt in the synthetic home.
func (w *e2eWorld) writeReceipt(body string) {
	writeFile(w.t, filepath.Join(append([]string{w.home}, append(append([]string(nil), hermes.ReceiptDirParts...), hermes.ReceiptLatestFile)...)...), body)
}

// killedReceipt is the receipt of a run that was killed while it built: still
// "running", its owner (pid 7001) is not in the process table.
const killedReceipt = `{
 "schema": 1, "started_at": "2026-09-27T20:55:05.123456+00:00", "finished_at": null,
 "pid": 7001, "outcome": "running", "update_id": "0f1e2d3c4b5a69780f1e2d3c4b5a6978",
 "stages": [
  {"name": "plan", "outcome": "success", "at": "2026-09-27T20:55:06.000000+00:00"},
  {"name": "apply", "outcome": "success", "at": "2026-09-27T20:55:30.000000+00:00"},
  {"name": "deps", "outcome": "success", "at": "2026-09-27T20:56:10.000000+00:00"}
 ]
}`

// noCommitUpdate is what `hermes update` prints when no commit is waiting but
// the work after the pull is redone: the fleet restart, the dependencies, the
// builds that are not current; it ends with "Already up to date!", never with
// "Update complete!".
func noCommitUpdate() []updateLine {
	return []updateLine{
		{0, "→ Update channel: main", nil},
		{2 * time.Second, fmt.Sprintf("→ Stopping Windows gateway (pid %d) so files can be replaced…", pidGateway), (*e2eWorld).removeGateways},
		{8 * time.Second, "→ Updating Python dependencies (uv sync)…", nil},
		{40 * time.Second, "  ✓ Python dependencies are current", nil},
		{5 * time.Second, "→ Building desktop packaged app…", nil},
		{190 * time.Second, "→ Packaging the desktop app…", nil},
		{10 * time.Second, "→ Restarting the gateway…", (*e2eWorld).startGateway},
		{15 * time.Second, "✓ Already up to date!", nil},
	}
}

func (w *e2eWorld) upToDateWorld() {
	w.apiStatus = "identical"
	w.update = noCommitUpdate()
}

func (w *e2eWorld) wantUnchangedHead() {
	w.t.Helper()
	if h := w.Head(); h != e2eOld {
		w.t.Errorf("head moved to %s", h)
	}
}

func TestE2EUnfinishedUpdateIsFinishedWithoutCommits(t *testing.T) {
	modes(t, func(t *testing.T, vt bool) {
		w := newWorld(t)
		w.vt = vt
		w.upToDateWorld()
		w.writeReceipt(killedReceipt)
		w.con.script(conStep{key: 'y'})
		w.cli()
		w.transcript("unfinished-finished")
		w.wantCode(apperr.OK)
		w.wantFlat(
			"last update", "did not finish (stopped during building the apps at yesterday 8:56 PM)",
			"The last update did not finish (stopped during building the apps at yesterday 8:56 PM). Hermes will close to finish it; that takes about",
			"Unfinished update finished and verified.", "RESULT: OK", "unchanged; no new commits", "running the new code "+e2eOld[:10])
		w.noScreen("Hermes is up to date", "WARN", "PROBLEM")
		w.wantUnchangedHead()
		if !w.ran(append([]string{w.launcher}, hermes.UpdateArgv...)...) {
			t.Error("hermes update did not run with the normal argv")
		}
		if c := w.app.Calls(); len(c) != 2 || c[0] != "close 4242" || c[1] != "launch Hermes.exe" {
			t.Errorf("app calls %v", c)
		}
		// a second run finds the update finished (the receipt a real Hermes
		// would have rewritten) and says "up to date" as before
	})
}

func TestE2EUnfinishedByTheOwnLogAlone(t *testing.T) {
	w := newWorld(t)
	w.upToDateWorld()
	// the log of the run before this one: it reached `hermes update`, was
	// packaging the app and was never heard of again; two runs after it said
	// "up to date" (they wrote "done" but never ran the update)
	logf := func(ts, msg, attrs string) string {
		return fmt.Sprintf("time=2026-09-27T%s.000+00:00 level=INFO msg=%q %s\n", ts, msg, attrs)
	}
	var b strings.Builder
	b.WriteString(logf("20:53:57", "==== Hermes safe update started", "pid=4440 check=false unattended=false"))
	b.WriteString(logf("20:55:05", "== Running: hermes update --yes --keep-stash --branch main", ""))
	b.WriteString(logf("20:56:09", "  | → Building desktop packaged app…", ""))
	b.WriteString(logf("20:57:12", "  | → Packaging the desktop app…", ""))
	b.WriteString(logf("21:12:11", "==== Hermes safe update started", "pid=10428 check=false unattended=false"))
	b.WriteString(logf("21:13:16", "==== done", "exit=0"))
	writeFile(t, filepath.Join(w.home, "logs", "safe-update.log"), b.String())
	w.con.script(conStep{key: 'y'})
	w.cli("--plain")
	w.wantCode(apperr.OK)
	w.wantScreen("did not finish (stopped during packaging the desktop app at yesterday 8:57 PM)", "Unfinished update finished and verified.")
	// the structured line is in the log, once
	logText, _ := os.ReadFile(filepath.Join(w.home, "logs", "safe-update.log"))
	if n := strings.Count(string(logText), `msg="previous update did not finish" signals=own-log at=2026-09-27T20:57:12Z stage="packaging the desktop app"`); n != 1 {
		t.Errorf("structured line found %d times:\n%s", n, logText)
	}
}

func TestE2EUnfinishedWithNewCommitsAddsOneSentence(t *testing.T) {
	w := newWorld(t)
	w.writeReceipt(killedReceipt)
	w.con.script(conStep{key: 'y'})
	w.cli("--plain")
	w.wantCode(apperr.OK)
	w.wantFlat("Hermes will close for the update. This update also finishes the previous update, which did not finish (stopped during building the apps at yesterday 8:56 PM).",
		"Hermes is updated and verified.", "v0.21.5 → v0.21.6")
	w.noScreen("Unfinished update finished", "No new commits")
	if got := w.Head(); got != e2eNew {
		t.Errorf("head %s", got)
	}
}

func TestE2EUnfinishedUnattendedFinishesWithoutAsking(t *testing.T) {
	w := newWorld(t)
	w.upToDateWorld()
	w.writeReceipt(killedReceipt)
	w.cli("--plain", "--unattended")
	w.wantCode(apperr.OK)
	w.wantScreen("Unfinished update finished and verified.")
	w.noScreen("Press Y")
	if w.con.prompts != 0 {
		t.Errorf("asked %d question(s)", w.con.prompts)
	}
	w.wantUnchangedHead()
}

func TestE2EUnfinishedCheckShowsTheRowAndTouchesNothing(t *testing.T) {
	w := newWorld(t)
	w.upToDateWorld()
	w.writeReceipt(killedReceipt)
	before := w.storeBytes()
	w.cli("--plain", "check")
	w.transcript("unfinished-check")
	w.wantCode(apperr.OK)
	w.wantFlat("did not finish (stopped during building the apps at yesterday 8:56 PM); run Hermes Safe Update to finish it",
		"The last update did not finish. Hermes was not touched.", "no new commits (the last update still needs finishing)")
	w.noScreen("Hermes is up to date")
	w.nothingChanged(before)
	if w.con.prompts != 0 {
		t.Errorf("check asked %d question(s)", w.con.prompts)
	}
}

// A receipt nobody can read changes nothing.
func TestE2EGarbageReceiptIsIgnored(t *testing.T) {
	w := newWorld(t)
	w.upToDateWorld()
	w.writeReceipt("{ this is not json")
	before := w.storeBytes()
	w.cli("--plain")
	w.wantCode(apperr.OK)
	w.wantScreen("Hermes is up to date")
	w.noScreen("last update")
	w.nothingChanged(before)
}

// A run that is going right now (its owner is alive) is the existing
// marker/lock business, not an unfinished update.
func TestE2EReceiptOfARunningUpdateIsNotUnfinished(t *testing.T) {
	w := newWorld(t)
	w.upToDateWorld()
	w.writeReceipt(killedReceipt)
	w.procs.Add(platform.Process{PID: 7001, Name: "python.exe"})
	before := w.storeBytes()
	w.cli("--plain")
	w.wantCode(apperr.OK)
	w.wantScreen("Hermes is up to date")
	w.noScreen("last update")
	w.nothingChanged(before)
}

func TestE2ERepairOfferedOnTheUpToDateScreenAndAccepted(t *testing.T) {
	w := newWorld(t)
	w.vt = true
	w.upToDateWorld()
	w.con.script(conStep{key: 'r'}) // one key: no second Y question
	w.cli()
	w.transcript("repair-offer-accepted")
	w.wantCode(apperr.OK)
	w.wantFlat("Hermes is up to date. Press R to repair this install: Hermes closes and the update steps run again (dependencies, builds, gateway restart), about",
		"Repair finished and verified.", "RESULT: OK", "unchanged; no new commits")
	w.noScreen("Press Y", "PROBLEM")
	w.wantUnchangedHead()
	if w.con.prompts != 1 {
		t.Errorf("asked %d question(s), want 1", w.con.prompts)
	}
	if c := w.app.Calls(); len(c) != 2 || c[0] != "close 4242" {
		t.Errorf("app calls %v", c)
	}
}

func TestE2ERepairCommandAsksTheUsualY(t *testing.T) {
	w := newWorld(t)
	w.upToDateWorld()
	w.con.script(conStep{key: 'y'})
	w.cli("--plain", "repair")
	w.transcript("repair-command")
	w.wantCode(apperr.OK)
	w.wantFlat("No new commits; repairing: the update steps run again.",
		"Hermes will close to repair this install: the update steps run again (dependencies, builds, gateway restart); that takes about",
		"Press Y to repair it now", "Repair finished and verified.")
	w.noScreen("Press R", "Hermes is up to date")
	w.wantUnchangedHead()
}

func TestE2ERepairCommandAnsweredNoChangesNothing(t *testing.T) {
	w := newWorld(t)
	w.upToDateWorld()
	w.con.script(conStep{key: 'n'})
	before := w.storeBytes()
	w.cli("--plain", "repair")
	w.wantCode(apperr.Cancelled)
	w.nothingChanged(before)
}

// The history keeps what a repair measured apart from what an update with
// commits teaches.
func TestE2ERepairRecordSaysItHadNoCommits(t *testing.T) {
	w := newWorld(t)
	w.upToDateWorld()
	w.con.script(conStep{key: 'y'})
	w.cli("--plain", "repair")
	w.wantCode(apperr.OK)
	b, err := os.ReadFile(filepath.Join(w.home, "logs", "safe-update-timings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"commits": 0`) {
		t.Errorf("the record does not say 0 commits:\n%s", b)
	}
}

// wantFlat checks text that the VT renderer may have wrapped over lines: the
// screen is read with every run of white space as one space.
func (w *e2eWorld) wantFlat(subs ...string) {
	w.t.Helper()
	flat := strings.Join(strings.Fields(w.screen()), " ")
	for _, sub := range subs {
		if !strings.Contains(flat, sub) {
			w.t.Errorf("screen lacks %q\n---- screen ----\n%s", sub, w.screen())
			return
		}
	}
}

// The pids of the world are made up (7001 is the receipt's owner): a creation-
// time lookup that reached the OS would open whatever real process has that
// number (Windows rounds pids to multiples of 4). The world's lookup knows no
// process, not even this one.
func TestE2EWorldNeverLooksUpARealProcess(t *testing.T) {
	w := newWorld(t)
	if ct, ok := w.environment().processCreated()(os.Getpid()); ok {
		t.Errorf("the world's environment found the creation time of a real process: %v", ct)
	}
	if _, ok := platform.ProcessCreated(os.Getpid()); !ok {
		t.Skip("this OS cannot read creation times at all, so the test above proves nothing here")
	}
}
