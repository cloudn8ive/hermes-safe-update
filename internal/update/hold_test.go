package update

import (
	"bytes"
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/mirror"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
	"github.com/cloudn8ive/hermes-safe-update/internal/ui"
)

// The stable hold (update.older_release: wait, the default): a run on the
// stable channel never moves Hermes back. The checkout is HEAD = oldSHA on
// v0.22.0+38, the release is stable v0.22.0 (behindSHA) unless a case says
// otherwise.

const (
	holdNewerText = "Waiting for stable: this build (v0.22.0+38.g1f6c02c) is 41 commits newer than stable v0.22.0; Safe Update stays on it until a stable release includes it."
	holdAskHint   = "update.older_release: ask"
	// every "on main for this run" way out says what main does (S2)
	holdMainAlso = "(this also updates Hermes to the newest main commits)"
)

// holdHarness is a stable run (update.channel: stable, update.older_release:
// wait) on a Hermes that words a release "available" (the check names the
// release; git decides). The release commit is in the checkout.
func holdHarness(t *testing.T) *harness {
	t.Helper()
	return holdHarnessCheck(t, "update-check-stable-available-ahead-v0217.txt")
}

// holdHarnessCheck is holdHarness for another answer of Hermes' release check.
func holdHarnessCheck(t *testing.T, fixtureName string) *harness {
	t.Helper()
	h := aheadHarnessCheck(t, execx.Result{Output: fixture(t, fixtureName)})
	h.cfg.Update.OlderRelease = config.OlderReleaseWait
	h.cfg.Tunables.ConfirmTimeoutS = 3 // a prompt nobody expected ends the test instead of hanging it
	return h
}

// hold relations, as git says them
func (h *harness) holdReleaseIsOlder() { h.releaseIsAncestor() }
func (h *harness) holdReleaseContains() {
	h.repo.ancestors = map[[2]string]bool{{oldSHA, behindSHA}: true}
}
func (h *harness) holdReleaseDiverged()   { h.repo.ancestors = nil }
func (h *harness) holdReleaseIsHead()     { h.repo.relSHA = oldSHA }
func (h *harness) holdShallowAndUnknown() { h.repo.shallow = true }

type holdCase struct {
	name  string
	setup func(h *harness)
	want  string // text of the wait outcome
}

// holdChecks are the cases where Hermes itself says "newer than the selected
// release" (v0.22.0 and later); the others get "available" (v0.21.6/7 wording).
var holdChecks = map[string]string{
	"Hermes: the checkout is newer than the release (counted by git)":           "update-check-stable-rollback.txt",
	"Hermes: the checkout is newer than the release (counted from the version)": "update-check-stable-rollback.txt",
}

func (c holdCase) harness(t *testing.T) *harness {
	if fx := holdChecks[c.name]; fx != "" {
		return holdHarnessCheck(t, fx)
	}
	return holdHarness(t)
}

var holdWaitCases = []holdCase{
	{"git: the release is an ancestor of this build", (*harness).holdReleaseIsOlder, holdNewerText},
	{"Hermes: the checkout is newer than the release (counted by git)", (*harness).releaseIsAncestor, holdNewerText},
	{"Hermes: the checkout is newer than the release (counted from the version)", func(h *harness) {
		h.repo.relRef, h.repo.relSHA = "", ""
	}, "is 38 commits newer than stable v0.22.0; Safe Update stays on it until a stable release includes it."},
	{"GitHub: the release is behind this build", func(h *harness) {
		h.repo.relRef, h.repo.relSHA = "", ""
		h.mirror = &fakeMirror{relCompare: &mirror.Compare{Status: "behind"}}
	}, "is 38 commits newer than stable v0.22.0;"},
	{"git: neither contains the other", (*harness).holdReleaseDiverged,
		"Waiting for stable: stable v0.22.0 does not contain this build (v0.22.0+38.g1f6c02c), and this build does not contain it; Safe Update stays on it until a stable release includes it."},
	{"GitHub: diverged", func(h *harness) {
		h.repo.relRef, h.repo.relSHA = "", ""
		h.mirror = &fakeMirror{relCompare: &mirror.Compare{Status: "diverged"}}
	}, "does not contain this build"},
	{"unproven: shallow clone", (*harness).holdShallowAndUnknown, "could not confirm that stable v0.22.0 contains this build"},
	{"unproven: git cannot say whether the clone is shallow", func(h *harness) { h.repo.shallowErr = true }, "could not confirm that stable v0.22.0 contains this build"},
	{"unproven: git cannot say", func(h *harness) { h.repo.ancestorErr = true }, "could not confirm that stable v0.22.0 contains this build"},
	{"unproven: the installed commit is unknown", func(h *harness) { h.repo.headErr = true }, "could not confirm that stable v0.22.0 contains this build"},
	{"unproven: the release is not in the checkout and GitHub cannot compare it", func(h *harness) {
		h.repo.relRef, h.repo.relSHA = "", ""
	}, "could not confirm that stable v0.22.0 contains this build (v0.22.0+38.g1f6c02c): the release commit is not in the checkout and could not be compared through GitHub either."},
}

// the ways a run goes: attended (the keys, if asked, would be Y), --unattended, check
func holdRuns() map[string]func(h *harness) error {
	return map[string]func(h *harness) error{
		"attended": func(h *harness) error {
			h.con.Keys = []platform.Key{key('y'), key('y')}
			return h.run()
		},
		"unattended": func(h *harness) error { return h.run("unattended") },
		"check": func(h *harness) error {
			h.mode = ModeCheck
			return h.run()
		},
	}
}

func TestStableHoldWaitsInEveryUnsafeRelation(t *testing.T) {
	for _, c := range holdWaitCases {
		for mode, run := range holdRuns() {
			t.Run(c.name+"/"+mode, func(t *testing.T) {
				h := c.harness(t)
				c.setup(h)
				var r *Run
				h.wrapRun(func(x *Run) { r = x })
				if err := run(h); err != nil {
					t.Fatalf("a wait is exit 0, got %v\n%s", err, h.output())
				}
				wantExit(t, nil, apperr.OK)
				h.mustContain(c.want, "waiting for stable", holdAskHint, "--allow-rollback", "Move back", "update.channel: main")
				h.nothingClosed(t)
				if strings.Contains(h.output(), "STOP") || strings.Contains(h.output(), "Press") || strings.Contains(h.output(), "BACK") {
					t.Errorf("a wait is neither a stop nor a question:\n%s", h.output())
				}
				if got := h.updateArgv(); got != "" {
					t.Errorf("hermes update ran: %q", got)
				}
				if h.sessions.calls != 0 {
					t.Error("sessions were read for a run that waits")
				}
				if r.Facts.Hold == nil {
					t.Error("no hold fact")
				}
				if mode == "attended" && len(h.con.Keys) != 2 {
					t.Errorf("an attended wait asked a question (keys left %d)", len(h.con.Keys))
				}
			})
		}
	}
}

// A release that contains this build, or is it, is updated to as before.
func TestStableHoldUpdatesWhenTheReleaseContainsThisBuild(t *testing.T) {
	cases := []holdCase{
		{"git: this build is an ancestor of the release", (*harness).holdReleaseContains, ""},
		{"GitHub: ahead", func(h *harness) {
			h.repo.relRef, h.repo.relSHA = "", ""
			h.mirror = &fakeMirror{relCompare: &mirror.Compare{Status: "ahead", Commits: 3}}
		}, ""},
		{"GitHub: identical", func(h *harness) {
			h.repo.relRef, h.repo.relSHA = "", ""
			h.mirror = &fakeMirror{relCompare: &mirror.Compare{Status: "identical"}}
		}, ""},
		{"git: the release is this build", (*harness).holdReleaseIsHead, ""},
	}
	for _, c := range cases {
		t.Run(c.name+"/attended", func(t *testing.T) {
			h := holdHarness(t)
			c.setup(h)
			h.con.Keys = []platform.Key{key('y')}
			if err := h.run(); err != nil {
				t.Fatalf("err=%v\n%s", err, h.output())
			}
			if h.updates != 1 || h.updateArgv() != "update --yes --keep-stash --channel stable" {
				t.Errorf("updates=%d argv=%q", h.updates, h.updateArgv())
			}
			if strings.Contains(h.output(), "Waiting for stable") {
				t.Errorf("waited although the release contains this build:\n%s", h.output())
			}
		})
		t.Run(c.name+"/unattended", func(t *testing.T) {
			h := holdHarness(t)
			c.setup(h)
			if err := h.run("unattended"); err != nil || h.updates != 1 {
				t.Fatalf("err=%v updates=%d\n%s", err, h.updates, h.output())
			}
		})
		t.Run(c.name+"/check", func(t *testing.T) {
			h := holdHarness(t)
			c.setup(h)
			h.mode = ModeCheck
			if err := h.run(); err != nil || h.updates != 0 {
				t.Fatalf("err=%v updates=%d\n%s", err, h.updates, h.output())
			}
			h.mustContain("Update available")
			if strings.Contains(h.output(), "Waiting for stable") {
				t.Errorf("check waited although the release contains this build:\n%s", h.output())
			}
		})
	}
}

// Hermes' own "up to date with the release" is untouched by the hold.
func TestStableHoldReleaseEqualIsUpToDate(t *testing.T) {
	h := holdHarnessCheck(t, "update-check-stable-uptodate.txt")
	if err := h.run("unattended"); err != nil || h.updates != 0 {
		t.Fatalf("err=%v updates=%d\n%s", err, h.updates, h.output())
	}
	h.mustContain("already at the latest release", "Hermes is up to date")
	if strings.Contains(h.output(), "Waiting for stable") {
		t.Errorf("an equal release is plain up to date:\n%s", h.output())
	}
}

// update.older_release: ask keeps the question (and --allow-rollback is the
// same for one run: the config value it sets).
func TestStableHoldAskKeepsTheRollbackQuestion(t *testing.T) {
	h := holdHarness(t)
	h.cfg.Update.OlderRelease = config.OlderReleaseAsk
	h.holdReleaseIsOlder()
	h.con.Keys = []platform.Key{key('y'), key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	h.mustContain("move Hermes BACK from v0.22.0+38.g1f6c02c to stable v0.22.0 (41 commits back)", "rollback confirmed")
	if strings.Contains(h.output(), "Waiting for stable") {
		t.Errorf("ask must not wait:\n%s", h.output())
	}
	if h.updates != 1 {
		t.Errorf("updates=%d", h.updates)
	}
}

// ask: unattended and check still stop, exit 4 (today's behaviour).
func TestStableHoldAskStillStopsUnattendedAndCheck(t *testing.T) {
	for _, mode := range []string{"unattended", "check"} {
		h := holdHarness(t)
		h.cfg.Update.OlderRelease = config.OlderReleaseAsk
		h.holdReleaseIsOlder()
		var err error
		if mode == "check" {
			h.mode = ModeCheck
			err = h.run()
		} else {
			err = h.run(mode)
		}
		wantExit(t, err, apperr.PreflightStop)
		h.nothingClosed(t)
	}
}

// The hold applies to the stable channel only: main (and a follow that
// resolves to main) never waits.
func TestStableHoldIsStableOnly(t *testing.T) {
	h := newHarness(t)
	h.cfg.Update.OlderRelease = config.OlderReleaseWait
	h.cfg.Tunables.ConfirmTimeoutS = 3
	h.cfg.Update.Channel = config.ChannelMain
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil || h.updates != 1 {
		t.Fatalf("err=%v updates=%d\n%s", err, h.updates, h.output())
	}
	if strings.Contains(h.output(), "Waiting for stable") {
		t.Errorf("main waits for nothing:\n%s", h.output())
	}
}

// follow, resolved to stable by Hermes' record: the hold applies too, and the
// plain `hermes update` (which follows the record) is never run.
func TestStableHoldFollowResolvedToStable(t *testing.T) {
	h := newHarness(t)
	h.cfg.Update.OlderRelease = config.OlderReleaseWait
	h.cfg.Tunables.ConfirmTimeoutS = 3
	h.cfg.Update.Channel = config.ChannelFollow
	h.channelHome(t, "stable", officialURL)
	h.hermesHelp(helpWithChannels)
	h.answer(execx.Result{Output: versionOnDisk}, "--version")
	h.answer(execx.Result{Output: fixture(t, "update-check-stable-rollback.txt")}, "update", "--check")
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	h.mustContain("Waiting for stable: this build (v0.22.0+38.g1f6c02c) is 38 commits newer than stable v0.22.0")
	if h.updates != 0 || h.updateArgv() != "" {
		t.Errorf("hermes update ran: %q", h.updateArgv())
	}
}

// ---- nothing that runs `hermes update` while waiting ----

func TestStableHoldRefusesRepairForceAndFinishing(t *testing.T) {
	t.Run("repair command", func(t *testing.T) {
		h := holdHarness(t)
		h.holdReleaseIsOlder()
		h.repair = true
		err := h.run()
		wantExit(t, err, apperr.PreflightStop)
		h.nothingClosed(t)
		h.mustContain("Repair is not available while Safe Update waits for stable", "move Hermes back", "repair on main for this run "+holdMainAlso+": hermes-safe-update repair --channel main", holdNewerText)
	})
	t.Run("--force", func(t *testing.T) {
		h := holdHarness(t)
		h.holdReleaseIsOlder()
		err := h.run("force")
		wantExit(t, err, apperr.PreflightStop)
		h.nothingClosed(t)
		h.mustContain("--force is not available while Safe Update waits for stable", "run on main for this run "+holdMainAlso+": hermes-safe-update --force --channel main")
	})
	t.Run("an unfinished update, attended", func(t *testing.T) {
		h := holdHarness(t)
		h.holdReleaseIsOlder()
		h.receipts = &fakeReceipts{prev: receiptDied()}
		h.con.Keys = []platform.Key{key('y'), key('y')}
		err := h.run()
		wantExit(t, err, apperr.PreflightStop)
		h.nothingClosed(t)
		h.mustContain("The last update did not finish, but it cannot be finished while Safe Update waits for stable", "finish it on main for this run "+holdMainAlso+": hermes-safe-update --channel main")
		if len(h.con.Keys) != 2 {
			t.Error("asked a question")
		}
	})
	t.Run("an unfinished update, unattended", func(t *testing.T) {
		h := holdHarness(t)
		h.holdReleaseIsOlder()
		h.receipts = &fakeReceipts{prev: receiptDied()}
		wantExit(t, h.run("unattended"), apperr.PreflightStop)
		h.nothingClosed(t)
	})
	t.Run("an unfinished update in check mode is shown and not refused", func(t *testing.T) {
		h := holdHarness(t)
		h.holdReleaseIsOlder()
		h.receipts = &fakeReceipts{prev: receiptDied()}
		h.mode = ModeCheck
		if err := h.run(); err != nil {
			t.Fatalf("err=%v\n%s", err, h.output())
		}
		h.mustContain(holdNewerText, "Last update", "Finish it", "not available while Safe Update waits for stable", "finish it on main now "+holdMainAlso+": hermes-safe-update --channel main")
	})
}

// No R = repair offer on the wait screen: the key would be consumed if asked.
func TestStableHoldOffersNoRepair(t *testing.T) {
	h := holdHarness(t)
	h.holdReleaseIsOlder()
	h.con.Keys = []platform.Key{key('r'), key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	if len(h.con.Keys) != 2 || strings.Contains(h.output(), "Press R") || h.updates != 0 {
		t.Errorf("a repair was offered (keys left %d, updates %d):\n%s", len(h.con.Keys), h.updates, h.output())
	}
}

// The wait is a green outcome: no amber row, nothing that reads as a failure.
func TestStableHoldCardIsGreen(t *testing.T) {
	h := holdHarness(t)
	h.holdReleaseIsOlder()
	var r *Run
	h.wrapRun(func(x *Run) { r = x })
	if err := h.run("unattended"); err != nil {
		t.Fatal(err)
	}
	card := r.holdCard()
	if card.OK == nil || !*card.OK {
		t.Errorf("card is not OK: %+v", card)
	}
	for _, row := range card.Rows {
		if row.Verdict != nil {
			t.Errorf("row %q has a verdict: a plain wait has no amber", row.Key)
		}
	}
	if card.Headline != holdNewerText {
		t.Errorf("headline %q", card.Headline)
	}
}

// The hold does not depend on the channel name Hermes prints: a release check
// that names no channel (or one this tool does not know) still compares with
// a release and would still move the checkout back.
func TestStableHoldDoesNotNeedAChannelName(t *testing.T) {
	const body = "→ This checkout is newer than the selected release: stable v0.22.0 (0123456789ab).\n" +
		"  `hermes update` moves it back to that release (`hermes update --set-channel main` follows every commit).\n"
	for name, check := range map[string]string{
		"no channel line":     body,
		"a channel not known": "→ Update channel: beta\n" + body,
	} {
		t.Run(name, func(t *testing.T) {
			h := aheadHarnessCheck(t, execx.Result{Output: check})
			h.cfg.Update.OlderRelease = config.OlderReleaseWait
			h.cfg.Tunables.ConfirmTimeoutS = 3
			h.holdReleaseIsOlder()
			if err := h.run("unattended"); err != nil {
				t.Fatalf("a wait is exit 0, got %v\n%s", err, h.output())
			}
			h.mustContain("Waiting for stable")
			if got := h.updateArgv(); got != "" {
				t.Errorf("hermes update ran: %q", got)
			}
		})
	}
}

// The pre-flight "updates" row says why nothing is updated, per relation.
func TestStableHoldUpdatesRowPerRelation(t *testing.T) {
	cases := []struct {
		name  string
		setup func(h *harness)
		row   string
	}{
		{"newer", (*harness).holdReleaseIsOlder,
			"none, this build is newer than stable v0.22.0; waiting for stable (Hermes release check)"},
		{"diverged", (*harness).holdReleaseDiverged,
			"none, stable v0.22.0 does not contain this build; waiting for stable (Hermes release check)"},
		{"unproven", (*harness).holdShallowAndUnknown,
			"none, could not confirm that stable v0.22.0 contains this build; waiting for stable (Hermes release check)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := holdHarness(t)
			c.setup(h)
			if err := h.run("unattended"); err != nil {
				t.Fatalf("err=%v\n%s", err, h.output())
			}
			h.mustContain(c.row)
		})
	}
}

// ---- the rest of the wait outcome ----

// Every way out of the wait (the plain wait and the three refusals) joins the
// early process listing, as every other end of the remote stage does.
func TestStableHoldJoinsTheEarlyProcessListing(t *testing.T) {
	cases := map[string]func(h *harness){
		"wait":       func(h *harness) {},
		"repair":     func(h *harness) { h.repair = true },
		"force":      func(h *harness) { h.cfg.Run.Force = true },
		"unfinished": func(h *harness) { h.receipts = &fakeReceipts{prev: receiptDied()} },
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			h := holdHarness(t)
			h.holdReleaseIsOlder()
			set(h)
			var r *Run
			h.wrapRun(func(x *Run) { r = x })
			_ = h.run("unattended")
			if r.early != nil {
				t.Error("the early process listing was not joined")
			}
		})
	}
}

// The wait leaves the run's facts as an up-to-date run would: the check is
// stored, and it no longer predicts an update.
func TestStableHoldStoresTheCheckAsNoUpdate(t *testing.T) {
	h := holdHarness(t)
	h.holdReleaseIsOlder()
	var r *Run
	h.wrapRun(func(x *Run) { r = x })
	if err := h.run("unattended"); err != nil {
		t.Fatal(err)
	}
	if c := r.Facts.Check; c.Behind == nil || *c.Behind || c.Source != "git" {
		t.Errorf("stored check = %+v, want Source git and Behind false", c)
	}
}

// The log names the relation, so a later read of the log says why a run waited.
func TestStableHoldLogsItsState(t *testing.T) {
	cases := []struct {
		name  string
		setup func(h *harness)
		want  string
	}{
		{"newer", (*harness).holdReleaseIsOlder, "state=newer"},
		{"diverged", (*harness).holdReleaseDiverged, "state=diverged"},
		{"unproven", (*harness).holdShallowAndUnknown, "state=unproven"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := holdHarness(t)
			c.setup(h)
			var buf bytes.Buffer
			h.logSink = &buf
			if err := h.run("unattended"); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(buf.String(), "stable hold: waiting for stable") || !strings.Contains(buf.String(), c.want) || !strings.Contains(buf.String(), "older_release=wait") {
				t.Errorf("log lacks %q:\n%s", c.want, buf.String())
			}
		})
	}
}

// Without Hermes' version the build is named by its commit.
func TestStableHoldNamesTheCommitWhenThereIsNoVersion(t *testing.T) {
	// aheadHarnessCheck, with a `hermes --version` that prints nothing readable
	h := newHarness(t)
	h.cfg.Update.Channel = config.ChannelStable
	h.cfg.Update.OlderRelease = config.OlderReleaseWait
	h.cfg.Tunables.ConfirmTimeoutS = 3
	h.hermesHelp(helpWithChannels)
	h.answer(execx.Result{Output: "no version here"}, "--version")
	h.answer(execx.Result{Output: fixture(t, "update-check-stable-available-ahead-v0217.txt")}, "update", "--check", "--channel", "stable")
	h.stableUpdateStream(t)
	h.repo.relRef, h.repo.relSHA = "refs/tags/v0.22.0", behindSHA
	h.holdReleaseIsOlder()
	if err := h.run("unattended"); err != nil {
		t.Fatal(err)
	}
	h.mustContain("Waiting for stable: this build (commit ")
}

// A check that names no release (an old wording, or main-style output on a
// release plan) cannot be compared: it waits, and says so.
func TestStableHoldWithoutAReleaseLabel(t *testing.T) {
	t.Run("Hermes says move back but names no release", func(t *testing.T) {
		h := aheadHarnessCheck(t, execx.Result{Output: "→ Update channel: stable\n  `hermes update` moves it back to that release.\n"})
		h.cfg.Update.OlderRelease = config.OlderReleaseWait
		h.cfg.Tunables.ConfirmTimeoutS = 3
		if err := h.run("unattended"); err != nil {
			t.Fatalf("err=%v\n%s", err, h.output())
		}
		h.mustContain("newer than the latest stable release; Safe Update stays on it",
			"none, this build is newer than the latest stable release; waiting for stable")
	})
	t.Run("an update is announced without a release", func(t *testing.T) {
		h := aheadHarnessCheck(t, execx.Result{Output: "Update available: 5 commits behind origin/main.\n"})
		h.cfg.Update.OlderRelease = config.OlderReleaseWait
		h.cfg.Tunables.ConfirmTimeoutS = 3
		if err := h.run("unattended"); err != nil {
			t.Fatalf("err=%v\n%s", err, h.output())
		}
		h.mustContain("could not confirm that the latest stable release contains this build",
			"Hermes named no release commit to compare")
		if got := h.updateArgv(); got != "" {
			t.Errorf("hermes update ran: %q", got)
		}
	})
}

// No git (no Repo) proves nothing: it waits, naming that reason.
func TestStableHoldWithoutGitWaits(t *testing.T) {
	h := holdHarness(t)
	h.wrapRun(func(x *Run) { x.Repo = nil })
	if err := h.run("unattended"); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	h.mustContain("Waiting for stable: Safe Update could not confirm", "git is not available for this checkout")
	if got := h.updateArgv(); got != "" {
		t.Errorf("hermes update ran: %q", got)
	}
}

// The end card of a wait names the channel, the way back and the log; an
// unfinished update is shown in amber, with why it is not finished here.
func TestStableHoldCardRows(t *testing.T) {
	rowsOf := func(c ui.Card) map[string]ui.Row {
		m := map[string]ui.Row{}
		for _, r := range c.Rows {
			m[r.Key] = r
		}
		return m
	}
	t.Run("plain wait", func(t *testing.T) {
		h := holdHarness(t)
		h.holdReleaseIsOlder()
		var r *Run
		h.wrapRun(func(x *Run) { r = x })
		if err := h.run("unattended"); err != nil {
			t.Fatal(err)
		}
		rows := rowsOf(r.holdCard())
		if !strings.HasPrefix(rows["Channel"].Value, "stable") {
			t.Errorf("Channel row = %+v", rows["Channel"])
		}
		if rows["Log"].Value != r.LogPath || r.LogPath == "" {
			t.Errorf("Log row = %+v, want %q", rows["Log"], r.LogPath)
		}
		if !strings.Contains(rows["Move back"].Value, "update.older_release: ask") {
			t.Errorf("Move back row = %+v", rows["Move back"])
		}
		for _, k := range []string{"Last update", "Finish it"} {
			if _, ok := rows[k]; ok {
				t.Errorf("row %q on a clean install", k)
			}
		}
	})
	t.Run("an unfinished update", func(t *testing.T) {
		h := holdHarness(t)
		h.holdReleaseIsOlder()
		h.receipts = &fakeReceipts{prev: receiptDied()}
		h.mode = ModeCheck
		var r *Run
		h.wrapRun(func(x *Run) { r = x })
		if err := h.run(); err != nil {
			t.Fatal(err)
		}
		rows := rowsOf(r.holdCard())
		for _, k := range []string{"Last update", "Finish it"} {
			row, ok := rows[k]
			if !ok || row.Verdict == nil || *row.Verdict != ui.VerdictAttention {
				t.Errorf("row %q = %+v, want present and amber", k, row)
			}
		}
	})
}

// A release that contains this build is announced and stored like any
// forward update: the hold only decides, it does not hide the update.
func TestStableHoldForwardUpdateIsAnnouncedAndStored(t *testing.T) {
	h := holdHarness(t)
	h.holdReleaseContains()
	h.mode = ModeCheck
	var r *Run
	h.wrapRun(func(x *Run) { r = x })
	if err := h.run(); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	h.mustContain("release v0.22.0 available")
	if c := r.Facts.Check; c.Behind == nil || !*c.Behind || c.Source != "git" {
		t.Errorf("stored check = %+v, want Source git and Behind true", c)
	}
}
