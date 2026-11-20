package update

import (
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// ---- S3: a persisted record plus "up to date" / "waits for the next release" ----

// Hermes' rule (hermes_cli/update_channel.py:192-200, source_releases.py:81-96)
// is that "waits for the next release" exists only without a persisted record.
// When Safe Update itself read a record, a run that could still run `hermes
// update` (finish, repair, --force, R) must first prove the release contains
// the build. recordHarness is follow + a stable record + that verdict.
func recordHarness(t *testing.T, record, fixtureName string) *harness {
	t.Helper()
	h := newHarness(t)
	h.cfg.Update.OlderRelease = config.OlderReleaseWait
	h.cfg.Tunables.ConfirmTimeoutS = 3
	h.cfg.Update.Channel = config.ChannelFollow
	h.channelHome(t, record, officialURL)
	h.hermesHelp(helpWithChannels)
	h.answer(execx.Result{Output: versionOnDisk}, "--version")
	h.answer(execx.Result{Output: fixture(t, fixtureName)}, "update", "--check")
	h.stableUpdateStream(t)
	h.repo.relRef, h.repo.relSHA = "refs/tags/v0.22.0", behindSHA
	return h
}

var recordVerdicts = []string{"update-check-stable-waits.txt", "update-check-stable-uptodate.txt"}

func TestStableHoldRecordWaitsRefusesWhatWouldRunTheUpdate(t *testing.T) {
	for _, fx := range recordVerdicts {
		for _, rel := range []struct {
			name  string
			setup func(h *harness)
		}{
			{"the release is older", (*harness).holdReleaseIsOlder},
			{"diverged", (*harness).holdReleaseDiverged},
			{"unproven", (*harness).holdShallowAndUnknown},
		} {
			t.Run(fx+"/"+rel.name+"/unfinished update, attended", func(t *testing.T) {
				h := recordHarness(t, "stable", fx)
				rel.setup(h)
				h.receipts = &fakeReceipts{prev: receiptDied()}
				h.con.Keys = []platform.Key{key('y'), key('y')}
				wantExit(t, h.run(), apperr.PreflightStop)
				h.nothingClosed(t)
				h.mustContain("Waiting for stable", "cannot be finished while Safe Update waits for stable")
				if len(h.con.Keys) != 2 {
					t.Error("asked a question")
				}
			})
			t.Run(fx+"/"+rel.name+"/unfinished update, unattended", func(t *testing.T) {
				h := recordHarness(t, "stable", fx)
				rel.setup(h)
				h.receipts = &fakeReceipts{prev: receiptDied()}
				wantExit(t, h.run("unattended"), apperr.PreflightStop)
				h.nothingClosed(t)
			})
			t.Run(fx+"/"+rel.name+"/repair", func(t *testing.T) {
				h := recordHarness(t, "stable", fx)
				rel.setup(h)
				h.repair = true
				wantExit(t, h.run(), apperr.PreflightStop)
				h.nothingClosed(t)
				h.mustContain("Repair is not available while Safe Update waits for stable")
			})
			t.Run(fx+"/"+rel.name+"/--force", func(t *testing.T) {
				h := recordHarness(t, "stable", fx)
				rel.setup(h)
				wantExit(t, h.run("force"), apperr.PreflightStop)
				h.nothingClosed(t)
				h.mustContain("--force is not available while Safe Update waits for stable")
			})
			t.Run(fx+"/"+rel.name+"/no R offer", func(t *testing.T) {
				h := recordHarness(t, "stable", fx)
				rel.setup(h)
				h.con.Terminal = true // the R offer needs a console
				h.con.Keys = []platform.Key{key('r'), key('y')}
				if err := h.run(); err != nil {
					t.Fatalf("err=%v\n%s", err, h.output())
				}
				if len(h.con.Keys) != 2 || strings.Contains(h.output(), "Press R") || h.updates != 0 {
					t.Errorf("a repair was offered (keys left %d, updates %d):\n%s", len(h.con.Keys), h.updates, h.output())
				}
				h.mustContain("Waiting for stable")
			})
		}
	}
}

// A release that is this build (or contains it) is not held: the finish goes
// on, with the argv Hermes follows its own record by (no channel flag).
func TestStableHoldRecordReleaseContainsBuildFinishesAsBefore(t *testing.T) {
	for _, fx := range recordVerdicts {
		for name, setup := range map[string]func(h *harness){
			"the release is this build":  (*harness).holdReleaseIsHead,
			"the release contains build": (*harness).holdReleaseContains,
		} {
			t.Run(fx+"/"+name, func(t *testing.T) {
				h := recordHarness(t, "stable", fx)
				setup(h)
				h.receipts = &fakeReceipts{prev: receiptDied()}
				h.con.Keys = []platform.Key{key('y'), key('y')}
				if err := h.run(); err != nil {
					t.Fatalf("err=%v\n%s", err, h.output())
				}
				if h.updates != 1 || h.updateArgv() != "update --yes --keep-stash" {
					t.Errorf("updates=%d argv=%q", h.updates, h.updateArgv())
				}
				if strings.Contains(h.output(), "Waiting for stable") {
					t.Errorf("waited although the release contains this build:\n%s", h.output())
				}
			})
		}
	}
}

// Control for the "no R offer" cases: when the release contains the build the
// offer is made on the same console, and R repairs with the bare argv.
func TestStableHoldRecordROfferedWhenTheReleaseContainsTheBuild(t *testing.T) {
	for _, fx := range recordVerdicts {
		t.Run(fx, func(t *testing.T) {
			h := recordHarness(t, "stable", fx)
			h.holdReleaseIsHead()
			h.con.Terminal = true
			h.con.Keys = []platform.Key{key('r'), key('y')}
			if err := h.run(); err != nil {
				t.Fatalf("err=%v\n%s", err, h.output())
			}
			h.mustContain("Press R to repair this install")
			if h.updates != 1 || h.updateArgv() != "update --yes --keep-stash" {
				t.Errorf("updates=%d argv=%q", h.updates, h.updateArgv())
			}
		})
	}
}

// Nothing that could run `hermes update`: the plain up-to-date screen is
// unchanged and git is not asked (the defence costs nothing there).
func TestStableHoldRecordPlainUpToDateAsksNothing(t *testing.T) {
	for _, fx := range recordVerdicts {
		t.Run(fx, func(t *testing.T) {
			h := recordHarness(t, "stable", fx)
			h.holdReleaseIsOlder()
			if err := h.run("unattended"); err != nil {
				t.Fatalf("err=%v\n%s", err, h.output())
			}
			h.nothingClosed(t)
			h.mustContain("Nothing to update")
			if strings.Contains(h.output(), "Waiting for stable") {
				t.Errorf("a plain up to date became a wait:\n%s", h.output())
			}
			if len(h.repo.resolved) != 0 {
				t.Errorf("git was asked about the release: %v", h.repo.resolved)
			}
		})
	}
}

// In check mode nothing runs, so nothing is held.
func TestStableHoldRecordCheckModeIsUntouched(t *testing.T) {
	h := recordHarness(t, "stable", "update-check-stable-waits.txt")
	h.holdReleaseIsOlder()
	h.receipts = &fakeReceipts{prev: receiptDied()}
	h.mode = ModeCheck
	if err := h.run(); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	if strings.Contains(h.output(), "Waiting for stable") {
		t.Errorf("check mode waited:\n%s", h.output())
	}
}

// update.older_release: ask is the old behaviour, record or not.
func TestStableHoldRecordAskIsUntouched(t *testing.T) {
	h := recordHarness(t, "stable", "update-check-stable-waits.txt")
	h.cfg.Update.OlderRelease = config.OlderReleaseAsk
	h.holdReleaseIsOlder()
	h.receipts = &fakeReceipts{prev: receiptDied()}
	h.con.Keys = []platform.Key{key('y'), key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	if h.updates != 1 || strings.Contains(h.output(), "Waiting for stable") {
		t.Errorf("updates=%d:\n%s", h.updates, h.output())
	}
}

// Pinning test: WITHOUT a record Hermes' "waits for the next release" is
// forward-only (Hermes' own rule), the finish of an unfinished update is the
// useful thing to do, and it goes on with the bare argv. Routing every
// "waits" verdict to the hold would refuse it.
func TestWaitsForNextWithoutARecordStillFinishesAnUnfinishedUpdate(t *testing.T) {
	h := recordHarness(t, "", "update-check-stable-waits.txt")
	h.holdReleaseIsOlder() // the relation would hold it, if the record were there
	h.receipts = &fakeReceipts{prev: receiptDied()}
	h.con.Keys = []platform.Key{key('y'), key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	if h.updates != 1 || h.updateArgv() != "update --yes --keep-stash" {
		t.Errorf("updates=%d argv=%q", h.updates, h.updateArgv())
	}
	if strings.Contains(h.output(), "Waiting for stable") {
		t.Errorf("held without a record:\n%s", h.output())
	}
}

// The relation check only looks: what it learns about the release (commit
// count) must not turn Hermes' "nothing to update" into a prediction of an
// update with commits.
func TestStableHoldRecordRelationCheckLeavesTheCheckAlone(t *testing.T) {
	h := recordHarness(t, "stable", "update-check-stable-uptodate.txt")
	h.holdReleaseContains()
	h.repo.count, h.repo.countOK = 5, true
	h.receipts = &fakeReceipts{prev: receiptDied()}
	var r *Run
	h.wrapRun(func(x *Run) { r = x })
	if err := h.run("unattended"); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	if len(h.repo.counted) == 0 {
		t.Fatal("the relation check did not count commits: the test checks nothing")
	}
	if c := r.Facts.Check.Commits; c != nil {
		t.Errorf("the check predicts %d commits for an up-to-date release", *c)
	}
}
