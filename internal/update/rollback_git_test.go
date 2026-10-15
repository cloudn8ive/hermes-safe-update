package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/mirror"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// Hermes v0.21.6 / v0.21.7 never say "This checkout is newer than the
// selected release": for a release that is OLDER than the checkout they print
// "Selected release available" and then `hermes update --yes` checks that
// release out, with no question. Safe Update decides with git
// (`merge-base --is-ancestor <release> HEAD`) in the remote stage, before
// anything is closed.

const behindSHA = "0123456789abcdef0123456789abcdef01234567" // the release v0.22.0 commit

// aheadHarness is a stable run on a Hermes that words everything "available";
// the checkout (HEAD = oldSHA) is on v0.22.0+38, the release is v0.22.0.
func aheadHarness(t *testing.T) *harness {
	t.Helper()
	return aheadHarnessCheck(t, execx.Result{Output: fixture(t, "update-check-stable-available-ahead-v0217.txt")})
}

// aheadHarnessCheck is aheadHarness with the answer of the release check given.
func aheadHarnessCheck(t *testing.T, check execx.Result) *harness {
	t.Helper()
	h := newHarness(t)
	h.cfg.Update.Channel = config.ChannelStable
	h.hermesHelp(helpWithChannels)
	h.answer(execx.Result{Output: versionOnDisk}, "--version")
	h.answer(check, "update", "--check", "--channel", "stable")
	h.stableUpdateStream(t)
	h.repo.relRef, h.repo.relSHA = "refs/tags/v0.22.0", behindSHA
	return h
}

// releaseIsAncestor makes git say the release is an ancestor of HEAD, 41
// commits back.
func (h *harness) releaseIsAncestor() {
	h.repo.ancestors = map[[2]string]bool{{behindSHA, oldSHA}: true}
	h.repo.pairs = map[[2]string]int{{behindSHA, oldSHA}: 41}
}

func TestRollbackFoundByGitWhenHermesSaysAvailable(t *testing.T) {
	t.Run("attended Y goes on", func(t *testing.T) {
		h := aheadHarness(t)
		h.releaseIsAncestor()
		h.con.Keys = []platform.Key{key('y'), key('y')} // the rollback question, then the usual confirmation
		var r *Run
		h.wrapRun(func(x *Run) { r = x })
		if err := h.run(); err != nil {
			t.Fatalf("err=%v\n%s", err, h.output())
		}
		h.mustContain("move Hermes BACK from v0.22.0+38.g1f6c02c to stable v0.22.0 (41 commits back)", "rollback confirmed")
		if h.updates != 1 || h.updateArgv() != "update --yes --keep-stash --channel stable" {
			t.Errorf("updates=%d argv=%q", h.updates, h.updateArgv())
		}
		if got := r.Facts.Channel.Check.Verdict; got != hermes.VerdictRollback {
			t.Errorf("the check card says %v, want a rollback", got)
		}
		if len(h.repo.ancestorAsks) != 1 || h.repo.ancestorAsks[0] != [2]string{behindSHA, oldSHA} {
			t.Errorf("git was asked %v", h.repo.ancestorAsks)
		}
	})
	t.Run("N, Enter, Esc, a timeout: exit 4 and nothing closed", func(t *testing.T) {
		for name, keys := range map[string][]platform.Key{"n": {key('n')}, "enter": {key('\r')}, "escape": {key(0x1b)}, "timeout": nil} {
			t.Run(name, func(t *testing.T) {
				h := aheadHarness(t)
				h.releaseIsAncestor()
				h.cfg.Tunables.ConfirmTimeoutS = 1
				h.con.Keys = keys
				wantExit(t, h.run(), apperr.PreflightStop)
				h.nothingClosed(t)
				h.mustContain("BACK from", "nothing was changed")
			})
		}
	})
	t.Run("--yes does not answer the question", func(t *testing.T) {
		h := aheadHarness(t)
		h.releaseIsAncestor()
		h.cfg.Tunables.ConfirmTimeoutS = 1
		wantExit(t, h.run("yes"), apperr.PreflightStop)
		h.nothingClosed(t)
	})
	t.Run("unattended never rolls back", func(t *testing.T) {
		h := aheadHarness(t)
		h.releaseIsAncestor()
		h.con.Keys = []platform.Key{key('y'), key('y')}
		wantExit(t, h.run("unattended"), apperr.PreflightStop)
		h.nothingClosed(t)
		h.mustContain("BACK from", "never rolls back", "hermes-safe-update run", "hermes update")
		if len(h.con.Keys) != 2 {
			t.Error("an unattended run asked")
		}
	})
	t.Run("--force does not get past it", func(t *testing.T) {
		h := aheadHarness(t)
		h.releaseIsAncestor()
		h.cfg.Tunables.ConfirmTimeoutS = 1
		wantExit(t, h.run("force", "unattended"), apperr.PreflightStop)
		h.nothingClosed(t)
	})
	t.Run("a check run reports it and asks nothing", func(t *testing.T) {
		h := aheadHarness(t)
		h.releaseIsAncestor()
		h.mode = ModeCheck
		h.con.Keys = []platform.Key{key('y')}
		wantExit(t, h.run(), apperr.PreflightStop)
		h.nothingClosed(t)
		h.mustContain("BACK from", "checkout is newer than release stable v0.22.0")
		if len(h.con.Keys) != 1 {
			t.Error("a check asked a question")
		}
	})
}

// Equal, a descendant, unrelated: all forward (or nothing to move), no question.
func TestForwardReleaseIsNotARollback(t *testing.T) {
	t.Run("the release is HEAD", func(t *testing.T) {
		h := aheadHarness(t)
		h.repo.relSHA = oldSHA
		h.con.Keys = []platform.Key{key('y')}
		if err := h.run(); err != nil {
			t.Fatalf("err=%v\n%s", err, h.output())
		}
		if len(h.repo.ancestorAsks) != 0 {
			t.Errorf("equal commits need no ancestry test: %v", h.repo.ancestorAsks)
		}
		if strings.Contains(h.output(), "BACK") || h.updates != 1 {
			t.Errorf("updates=%d\n%s", h.updates, h.output())
		}
	})
	for name, ancestors := range map[string]map[[2]string]bool{
		"the release is ahead of HEAD (HEAD is its ancestor)": {{oldSHA, behindSHA}: true},
		"unrelated histories": nil,
	} {
		t.Run(name, func(t *testing.T) {
			h := aheadHarness(t)
			h.repo.ancestors = ancestors
			h.con.Keys = []platform.Key{key('y')}
			if err := h.run(); err != nil {
				t.Fatalf("err=%v\n%s", err, h.output())
			}
			if strings.Contains(h.output(), "BACK") || h.updates != 1 || len(h.repo.ancestorAsks) != 1 {
				t.Errorf("updates=%d asks=%v\n%s", h.updates, h.repo.ancestorAsks, h.output())
			}
		})
	}
}

// The release commit is not in the checkout: in a full clone (a partial
// blob:none clone has every commit) an unknown commit cannot be an ancestor.
func TestReleaseNotLocalFullCloneIsForward(t *testing.T) {
	h := aheadHarness(t)
	h.repo.relRef, h.repo.relSHA = "", ""
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	if h.repo.shallowAsks != 1 || len(h.repo.ancestorAsks) != 0 {
		t.Errorf("shallow asked %d times, ancestry %v", h.repo.shallowAsks, h.repo.ancestorAsks)
	}
	if strings.Contains(h.output(), "BACK") || h.updates != 1 {
		t.Errorf("updates=%d\n%s", h.updates, h.output())
	}
}

// A shallow clone (or git failing) cannot prove it: possible rollback, so the
// attended run asks and the unattended run stops.
func TestReleaseNotLocalShallowIsPossibleRollback(t *testing.T) {
	cases := map[string]func(h *harness){
		"shallow clone":  func(h *harness) { h.repo.shallow = true },
		"git cannot say": func(h *harness) { h.repo.shallowErr = true },
		"ancestry test failing": func(h *harness) {
			h.repo.relRef, h.repo.relSHA = "refs/tags/v0.22.0", behindSHA
			h.repo.ancestorErr = true
		},
	}
	for name, setup := range cases {
		t.Run(name+": attended Y goes on", func(t *testing.T) {
			h := aheadHarness(t)
			h.repo.relRef, h.repo.relSHA = "", ""
			setup(h)
			h.con.Keys = []platform.Key{key('y'), key('y')}
			if err := h.run(); err != nil {
				t.Fatalf("err=%v\n%s", err, h.output())
			}
			h.mustContain("might move Hermes BACK from v0.22.0+38.g1f6c02c to stable v0.22.0", "Press Y to go on", "rollback confirmed")
			if h.updates != 1 {
				t.Errorf("updates=%d", h.updates)
			}
		})
		t.Run(name+": attended N stops", func(t *testing.T) {
			h := aheadHarness(t)
			h.repo.relRef, h.repo.relSHA = "", ""
			setup(h)
			h.con.Keys = []platform.Key{key('n')}
			wantExit(t, h.run(), apperr.PreflightStop)
			h.nothingClosed(t)
		})
		t.Run(name+": unattended stops", func(t *testing.T) {
			h := aheadHarness(t)
			h.repo.relRef, h.repo.relSHA = "", ""
			setup(h)
			wantExit(t, h.run("unattended"), apperr.PreflightStop)
			h.nothingClosed(t)
			h.mustContain("might move Hermes BACK", "does not go on when it cannot rule that out")
		})
	}
}

// GitHub's compare of HEAD with the tag says "behind" = the tag is an
// ancestor of HEAD: a rollback, even where the commit is not in the checkout
// (a shallow clone).
func TestReleaseBehindPerGitHubIsARollback(t *testing.T) {
	h := aheadHarness(t)
	h.repo.relRef, h.repo.relSHA = "", ""
	h.repo.shallow = true
	m := &fakeMirror{}
	m.st = mirror.Status{OK: true, ConfiguredPath: `X:\m`, JobActive: true}
	m.relCompare = &mirror.Compare{Status: "behind"}
	h.mirror = m
	h.con.Keys = []platform.Key{key('n')}
	wantExit(t, h.run(), apperr.PreflightStop)
	h.nothingClosed(t)
	h.mustContain("would move Hermes BACK from v0.22.0+38.g1f6c02c to stable v0.22.0")
	if h.repo.shallowAsks != 0 {
		t.Error("GitHub had answered: no need to ask whether the clone is shallow")
	}
}

// The release commit IS in the checkout but git says "not an ancestor": in a
// shallow clone that is a guess (a release behind the shallow boundary is an
// ancestor git cannot see), so it is unproven, like a release that is not
// local. A full clone trusts the answer (TestForwardReleaseIsNotARollback).
func TestLocalReleaseNotAncestorInShallowCloneIsUnproven(t *testing.T) {
	setup := func(t *testing.T) *harness {
		h := aheadHarness(t) // the release commit is local; ancestors is empty
		h.repo.shallow = true
		return h
	}
	t.Run("attended Y goes on", func(t *testing.T) {
		h := setup(t)
		h.con.Keys = []platform.Key{key('y'), key('y')}
		if err := h.run(); err != nil {
			t.Fatalf("err=%v\n%s", err, h.output())
		}
		h.mustContain("might move Hermes BACK from v0.22.0+38.g1f6c02c to stable v0.22.0", "Press Y to go on")
		if h.updates != 1 {
			t.Errorf("updates=%d", h.updates)
		}
	})
	t.Run("attended N stops", func(t *testing.T) {
		h := setup(t)
		h.con.Keys = []platform.Key{key('n')}
		wantExit(t, h.run(), apperr.PreflightStop)
		h.nothingClosed(t)
	})
	t.Run("unattended stops", func(t *testing.T) {
		h := setup(t)
		wantExit(t, h.run("unattended"), apperr.PreflightStop)
		h.nothingClosed(t)
		h.mustContain("might move Hermes BACK", "does not go on when it cannot rule that out")
	})
	t.Run("a check run stops", func(t *testing.T) {
		h := setup(t)
		h.mode = ModeCheck
		wantExit(t, h.run(), apperr.PreflightStop)
		h.nothingClosed(t)
	})
	t.Run("git cannot say whether it is shallow", func(t *testing.T) {
		h := aheadHarness(t)
		h.repo.shallowErr = true
		wantExit(t, h.run("unattended"), apperr.PreflightStop)
		h.nothingClosed(t)
	})
	t.Run("an ancestor stays a proven rollback even when shallow", func(t *testing.T) {
		h := setup(t)
		h.releaseIsAncestor()
		h.con.Keys = []platform.Key{key('n')}
		wantExit(t, h.run(), apperr.PreflightStop)
		h.mustContain("would move Hermes BACK from v0.22.0+38.g1f6c02c to stable v0.22.0 (41 commits back)")
	})
}

// GitHub's compare of HEAD with the tag settles what a shallow clone cannot:
// ahead, identical and diverged all mean the tag is not behind HEAD (Hermes
// treats diverged as an upgrade target), so no question and no stop, even
// unattended; the clone is not even asked whether it is shallow.
func TestReleaseForwardPerGitHubIsNotAQuestion(t *testing.T) {
	for _, status := range []string{"ahead", "identical", "diverged"} {
		for _, flags := range [][]string{nil, {"unattended"}} {
			t.Run(status+" "+strings.Join(flags, "+"), func(t *testing.T) {
				h := aheadHarness(t)
				h.repo.relRef, h.repo.relSHA = "", ""
				h.repo.shallow = true
				m := &fakeMirror{}
				m.st = mirror.Status{OK: true, ConfiguredPath: `X:\m`, JobActive: true}
				m.relCompare = &mirror.Compare{Status: status}
				h.mirror = m
				h.con.Keys = []platform.Key{key('y')}
				if err := h.run(flags...); err != nil {
					t.Fatalf("err=%v\n%s", err, h.output())
				}
				if strings.Contains(h.output(), "BACK") || h.updates != 1 {
					t.Errorf("updates=%d\n%s", h.updates, h.output())
				}
				if h.repo.shallowAsks != 0 {
					t.Error("GitHub had answered: no need to ask whether the clone is shallow")
				}
			})
		}
	}
}

// With the installed commit unknown nothing can be compared: never the
// "BACK" message (and never a silent forward either). Attended asks in
// neutral words, unattended stops.
func TestUnknownInstalledCommitIsUnprovenWithNeutralWords(t *testing.T) {
	setup := func(t *testing.T) *harness {
		h := aheadHarness(t)
		h.repo.headErr = true
		return h
	}
	t.Run("attended Y goes on", func(t *testing.T) {
		h := setup(t)
		h.con.Keys = []platform.Key{key('y'), key('y')}
		_ = h.run() // the verify step has no commit to expect: its verdict is not what this tests
		h.mustContain("cannot tell whether updating to stable v0.22.0 is an update or a downgrade", "Press Y to go on")
		if strings.Contains(h.output(), "BACK") || h.updates != 1 {
			t.Errorf("updates=%d\n%s", h.updates, h.output())
		}
		if len(h.repo.ancestorAsks) != 0 {
			t.Errorf("git was asked with no installed commit: %v", h.repo.ancestorAsks)
		}
	})
	t.Run("unattended stops", func(t *testing.T) {
		h := setup(t)
		wantExit(t, h.run("unattended"), apperr.PreflightStop)
		h.nothingClosed(t)
		h.mustContain("cannot tell whether updating", "does not go on when it cannot rule that out")
		if strings.Contains(h.output(), "BACK") {
			t.Errorf("the BACK message with no installed commit:\n%s", h.output())
		}
	})
}

// Hermes' own rollback wording keeps working next to the git test; the number
// of commits comes from git when the release is in the checkout.
func TestRollbackWordingStillGuardsAndCountsFromGit(t *testing.T) {
	h := rollbackHarness(t)
	h.repo.relRef, h.repo.relSHA = "refs/tags/v0.22.0", behindSHA
	h.repo.pairs = map[[2]string]int{{behindSHA, oldSHA}: 41}
	h.cfg.Tunables.ConfirmTimeoutS = 1
	wantExit(t, h.run(), apperr.PreflightStop)
	h.nothingClosed(t)
	h.mustContain("BACK from v0.22.0+38.g1f6c02c to stable v0.22.0 (41 commits back)")
}

// A forward release is untouched by all of this: UpToDate / waits never
// reach the git test.
func TestNoAncestryTestWhenNothingWouldMove(t *testing.T) {
	for _, file := range []string{"update-check-stable-waits.txt", "update-check-stable-uptodate.txt"} {
		h := newHarness(t)
		h.cfg.Update.Channel = config.ChannelStable
		h.hermesHelp(helpWithChannels)
		h.answer(execx.Result{Output: fixture(t, file)}, "update", "--check", "--channel", "stable")
		if err := h.run(); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if len(h.repo.ancestorAsks)+h.repo.shallowAsks != 0 {
			t.Errorf("%s: git was asked", file)
		}
	}
}

// ---- --force never skips the stop on an unreadable release check ----

func TestForceDoesNotSkipAFailedReleaseCheck(t *testing.T) {
	cases := map[string]execx.Result{
		"non-zero exit": {Code: 2, Output: "Traceback: boom"},
		// a check that timed out is a failed check even if it printed a verdict first
		"timeout": {Code: 0, TimedOut: true, Output: fixture0()},
	}
	for name, res := range cases {
		for _, flags := range [][]string{nil, {"force"}, {"force", "unattended"}, {"force", "yes"}} {
			t.Run(name+" "+strings.Join(flags, "+"), func(t *testing.T) {
				h := aheadHarnessCheck(t, res)
				h.con.Keys = []platform.Key{key('y'), key('y')}
				wantExit(t, h.run(flags...), apperr.PreflightStop)
				h.nothingClosed(t)
				h.mustContain("could not check for updates", "nothing was changed")
			})
		}
	}
}

// fixture0 is a readable verdict (so only the timeout can be what stops).
func fixture0() string {
	return "→ Update channel: stable\n→ Selected release available: stable v0.22.1 (abcdef012345)\n  Run `hermes update` to install it.\n"
}

// --force still runs a checkout that Hermes says is up to date (as on main).
func TestForceStillRunsAReadableUpToDateRelease(t *testing.T) {
	h := newHarness(t)
	h.cfg.Update.Channel = config.ChannelStable
	h.hermesHelp(helpWithChannels)
	h.answer(execx.Result{Output: fixture(t, "update-check-stable-uptodate.txt")}, "update", "--check", "--channel", "stable")
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run("force"); err != nil {
		t.Fatal(err)
	}
	if h.updates != 1 {
		t.Errorf("--force did not run: %d", h.updates)
	}
}

// ---- capability probe fallbacks ----

// sourceFiles puts the two files of a channel-capable Hermes into the harness
// checkout (channelHome made the directory).
func (h *harness) sourceFiles(t *testing.T) {
	t.Helper()
	dir := filepath.Join(h.install.Checkout, "hermes_cli", "subcommands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	put := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put(filepath.Join(h.install.Checkout, "hermes_cli", "update_channel.py"), "# channels\n")
	put(filepath.Join(dir, "update.py"), "    add_argument(\"--set-channel\")\n    add_argument(\"--install-id\")\n    add_argument(\"--channel\")\n")
}

// A slow launcher made the help time out. The checkout's own source says it
// has channels, so a stable record is followed instead of silently moving a
// stable user to main.
func TestFollowWithHelpTimeoutUsesTheSourceFiles(t *testing.T) {
	for _, withID := range []bool{true, false} {
		name := "install-id answers"
		if !withID {
			name = "install-id fails too, record found by path"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			// a fork origin: without the record the default would be main
			if withID {
				h.channelHome(t, "stable", "https://github.com/someone/fork.git")
			} else {
				h.channelHomeFiles(t, "stable", "https://github.com/someone/fork.git")
				h.answer(execx.Result{Code: 2, Output: "Traceback"}, "update", "--install-id")
			}
			h.sourceFiles(t)
			if !withID {
				cfg := "update:\n  installs:\n    a1b2c3d4e5f60718:\n      path: '" + h.install.Checkout + string(filepath.Separator) + "'\n      channel: stable\n"
				if err := os.WriteFile(filepath.Join(h.install.Home, "config.yaml"), []byte(cfg), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			h.answer(execx.Result{Code: execx.CodeTimeout, TimedOut: true}, "update", "--help")
			h.answer(execx.Result{Output: fixture(t, "update-check-stable-available.txt")}, "update", "--check")
			h.stableUpdateStream(t)
			h.con.Keys = []platform.Key{key('y')}
			var r *Run
			h.wrapRun(func(x *Run) { r = x })
			if err := h.run(); err != nil {
				t.Fatalf("err=%v\n%s", err, h.output())
			}
			if got := h.updateArgv(); got != "update --yes --keep-stash" {
				t.Errorf("update argv %q: a stable record is followed with no channel flag", got)
			}
			if got := h.ranCheck(); len(got) != 1 || got[0] != "update --check" {
				t.Errorf("checks %v", got)
			}
			p := r.Facts.Channel.Plan
			if p.Caps.Source != hermes.CapsFromSource || !p.Caps.Channel || p.Resolved.Channel != "stable" || p.Resolved.Source != hermes.SourceRecord {
				t.Errorf("plan: %+v", p)
			}
		})
	}
}

// Neither the help nor the source files show channels: that is a Hermes from
// before channels, and today's main argv is right for it.
func TestFollowWithoutAnyCapabilityEvidenceIsMain(t *testing.T) {
	h := newHarness(t)
	h.channelHome(t, "stable", officialURL) // no source files in the checkout
	h.answer(execx.Result{Code: execx.CodeTimeout, TimedOut: true}, "update", "--help")
	h.con.Keys = []platform.Key{key('y')}
	var r *Run
	h.wrapRun(func(x *Run) { r = x })
	if err := h.run(); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	if got := h.updateArgv(); got != "update --yes --keep-stash --branch main" {
		t.Errorf("update argv %q", got)
	}
	if p := r.Facts.Channel.Plan; p.Caps.Source != hermes.CapsFromNone || p.Caps.Err != "timed out" {
		t.Errorf("caps: %+v", p.Caps)
	}
}

// Mode stable with a failed help used to stop ("could not read the help"); the
// source files now answer.
func TestStableWithHelpTimeoutUsesTheSourceFiles(t *testing.T) {
	h := newHarness(t)
	h.cfg.Update.Channel = config.ChannelStable
	h.channelHome(t, "", officialURL)
	h.sourceFiles(t)
	h.answer(execx.Result{Code: execx.CodeTimeout, TimedOut: true}, "update", "--help")
	h.answer(execx.Result{Output: fixture(t, "update-check-stable-available.txt")}, "update", "--check", "--channel", "stable")
	h.stableUpdateStream(t)
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	if got := h.updateArgv(); got != "update --yes --keep-stash --channel stable" {
		t.Errorf("update argv %q", got)
	}
}

// The log says which source decided.
func TestChannelDecisionLogNamesTheCapsSource(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(h *harness, t *testing.T)
		want  string
	}{
		{"help", func(h *harness, t *testing.T) { h.hermesHelp(helpWithChannels) }, "caps_source=help"},
		{"source files", func(h *harness, t *testing.T) {
			h.sourceFiles(t)
			h.answer(execx.Result{Code: execx.CodeTimeout, TimedOut: true}, "update", "--help")
		}, `caps_source="source files"`},
		{"none", func(h *harness, t *testing.T) {
			h.answer(execx.Result{Code: execx.CodeTimeout, TimedOut: true}, "update", "--help")
		}, "caps_source=none"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			var sink strings.Builder
			h.logSink = &sink
			h.channelHome(t, "main", officialURL)
			c.setup(h, t)
			h.con.Keys = []platform.Key{key('y')}
			if err := h.run(); err != nil {
				t.Fatalf("err=%v\n%s", err, h.output())
			}
			if !strings.Contains(sink.String(), c.want) {
				t.Errorf("the log lacks %q:\n%s", c.want, sink.String())
			}
		})
	}
}

// ---- a cancelled run never gets to the update ----

// A cancelled run ends in the remote stage: the plan an unfinished probe
// would give is a main plan, and nothing may go on to the check or the update
// with it.
func TestCancelledRemoteStageReturnsTheCancelNotAMainPlan(t *testing.T) {
	for _, mode := range []string{config.ChannelFollow, config.ChannelStable, config.ChannelMain} {
		t.Run(mode, func(t *testing.T) {
			f := &execx.Fake{}
			cfg := &config.Config{File: config.Defaults()}
			cfg.Update.Channel = mode
			r := NewRun(Deps{Config: cfg, Runner: f, Repo: &fakeRepo{head: oldSHA}})
			r.Facts.Install = hermes.Install{Launcher: tLauncher, Home: tHome}
			// a probe that has not finished (follow / stable): its result must not be used
			if mode != config.ChannelMain {
				r.chanProbe = make(chan chanProbe)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err := stageRemote(ctx, r)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want the cancellation", err)
			}
			if len(f.Calls()) != 0 {
				t.Errorf("a cancelled run ran %v", f.Calls()[0].Argv)
			}
			if got := r.Facts.Channel.Plan; got.Mode != "" || len(got.UpdateArgv) != 0 {
				t.Errorf("a plan was made: %+v", got)
			}
		})
	}
}
