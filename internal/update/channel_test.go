package update

import (
	"context"
	"errors"
	"fmt"
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

// Release-channel tests of the flow: which argv runs per mode, how each
// verdict of Hermes' own `--check` ends the remote stage, the rollback guard.
// The Hermes texts are the synthetic fixtures of internal/hermes.

const (
	helpWithChannels = "usage: hermes update [-h] [--yes] [--keep-stash] [--check]\n  [--branch BRANCH] [--channel CHANNEL] [--set-channel CHANNEL] [--install-id]\n"
	helpLegacy       = "usage: hermes update [-h] [--yes] [--keep-stash] [--check]\n  [--branch BRANCH]\n"
	chanInstallID    = "0f1e2d3c4b5a6978"
	officialURL      = "https://github.com/NousResearch/hermes-agent.git"
	versionOnDisk    = "Hermes Agent v0.22.0+38.g1f6c02c (2026.10.14)\nInstall directory: X\nInstall method: git\n"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "hermes", "testdata", "contracts", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// answer scripts one `hermes <argv...>` call.
func (h *harness) answer(res execx.Result, argv ...string) {
	h.runner.On(append([]string{tLauncher}, argv...), res)
}

// hermesHelp scripts `hermes update --help`.
func (h *harness) hermesHelp(text string) { h.answer(execx.Result{Output: text}, "update", "--help") }

// ranHermes counts the calls of `hermes <argv...>` (exact argv).
func (h *harness) ranHermes(argv ...string) int {
	want := strings.Join(append([]string{tLauncher}, argv...), "\x00")
	n := 0
	for _, c := range h.runner.Calls() {
		if strings.Join(c.Argv, "\x00") == want {
			n++
		}
	}
	return n
}

func (h *harness) ranCheck() []string {
	var out []string
	for _, c := range h.runner.Calls() {
		if len(c.Argv) > 2 && c.Argv[1] == "update" && c.Argv[2] == "--check" {
			out = append(out, strings.Join(c.Argv[1:], " "))
		}
	}
	return out
}

func (h *harness) updateArgv() string {
	for _, c := range h.runner.Calls() {
		if len(c.Argv) > 2 && c.Argv[1] == "update" && c.Argv[2] == "--yes" {
			return strings.Join(c.Argv[1:], " ")
		}
	}
	return ""
}

// channelHome puts a real home and checkout on disk (config.yaml with the
// record, .git/config with the origin) and scripts --install-id, so the
// offline channel read works. record "" = no record.
func (h *harness) channelHome(t *testing.T, record, origin string) {
	t.Helper()
	h.channelHomeFiles(t, record, origin)
	h.answer(execx.Result{Output: fmt.Sprintln(chanInstallID)}, "update", "--install-id")
}

// channelHomeFiles is channelHome without the --install-id answer.
func (h *harness) channelHomeFiles(t *testing.T, record, origin string) {
	t.Helper()
	root := t.TempDir()
	h.install.Home = filepath.Join(root, "home")
	h.install.Checkout = filepath.Join(h.install.Home, "hermes-agent")
	if err := os.MkdirAll(filepath.Join(h.install.Checkout, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(h.install.Checkout, ".git", "config"), "[remote \"origin\"]\n\turl = "+origin+"\n")
	if record != "" {
		write(filepath.Join(h.install.Home, "config.yaml"), "update:\n  installs:\n    "+chanInstallID+":\n      channel: "+record+"\n")
	}
}

func (h *harness) stableUpdateStream(t *testing.T) {
	h.onUpdate(0, fixture(t, "update-stable-switching.txt"), true)
}

func wantExit(t *testing.T, err error, code int) {
	t.Helper()
	if apperr.ExitCode(err) != code {
		t.Fatalf("exit %d (err=%v), want %d", apperr.ExitCode(err), err, code)
	}
}

// nothingClosed is the rule behind every pre-flight stop: no close, no
// marker, no update.
func (h *harness) nothingClosed(t *testing.T) {
	t.Helper()
	if h.updates != 0 || len(h.app.Calls()) != 0 || h.marker.claims != 0 || h.gw.stops != 0 {
		t.Errorf("something changed: updates=%d app=%v marker=%+v gateway stops=%d", h.updates, h.app.Calls(), h.marker, h.gw.stops)
	}
}

func TestUpdateRunsExpectedArgvPerMode(t *testing.T) {
	const (
		mainArgv   = "update --yes --keep-stash --branch main"
		stableArgv = "update --yes --keep-stash --channel stable"
		bareArgv   = "update --yes --keep-stash"
	)
	cases := []struct {
		name   string
		mode   string
		help   string // "" = the probe gets no answer
		record string // follow: channel record ("" = none)
		origin string
		check  string // wanted check argv (the one that must have run)
		update string
		out    string // answer of the release check, when one runs
	}{
		{"main", config.ChannelMain, helpWithChannels, "", officialURL, "update --check --branch main", mainArgv, ""},
		{"main on a legacy Hermes", config.ChannelMain, helpLegacy, "", officialURL, "update --check --branch main", mainArgv, ""},
		{"stable", config.ChannelStable, helpWithChannels, "", officialURL, "update --check --channel stable", stableArgv, "update-check-stable-available.txt"},
		{"follow, record main", config.ChannelFollow, helpWithChannels, "main", officialURL, "update --check --branch main", mainArgv, ""},
		{"follow, no record, fork origin", config.ChannelFollow, helpWithChannels, "", "https://github.com/someone/fork.git", "update --check --branch main", mainArgv, ""},
		{"follow, no record, official origin", config.ChannelFollow, helpWithChannels, "", officialURL, "update --check", bareArgv, "update-check-stable-available.txt"},
		{"follow, record stable", config.ChannelFollow, helpWithChannels, "stable", "https://github.com/someone/fork.git", "update --check", bareArgv, "update-check-stable-available.txt"},
		{"follow on a legacy Hermes", config.ChannelFollow, helpLegacy, "", officialURL, "update --check --branch main", mainArgv, ""},
		{"follow when the probe fails", config.ChannelFollow, "", "", officialURL, "update --check --branch main", mainArgv, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.cfg.Update.Channel = c.mode
			h.channelHome(t, c.record, c.origin)
			if c.help != "" {
				h.hermesHelp(c.help)
			}
			if c.out != "" {
				h.answer(execx.Result{Output: fixture(t, c.out)}, "update", "--check")
				h.answer(execx.Result{Output: fixture(t, c.out)}, "update", "--check", "--channel", "stable")
				h.stableUpdateStream(t)
			}
			h.con.Keys = []platform.Key{key('y')}
			if err := h.run(); err != nil {
				t.Fatalf("err=%v\n%s", err, h.output())
			}
			if got := h.updateArgv(); got != c.update {
				t.Errorf("update argv %q, want %q", got, c.update)
			}
			if got := h.ranCheck(); len(got) != 1 || got[0] != c.check {
				t.Errorf("checks run %v, want only %q", got, c.check)
			}
			for _, a := range h.runner.Calls() {
				if strings.Contains(strings.Join(a.Argv, " "), "set-channel") {
					t.Errorf("Safe Update ran %v", a.Argv)
				}
			}
			h.mustContain("RESULT: OK")
		})
	}
}

// Mode main needs no probe at all: it never asks Hermes about channels.
func TestMainModeNeverProbesChannels(t *testing.T) {
	h := newHarness(t)
	h.cfg.Update.Channel = config.ChannelMain
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err=%v", err)
	}
	if h.ranHermes("update", "--help")+h.ranHermes("update", "--install-id") != 0 {
		t.Error("main mode probed Hermes for channels")
	}
}

// ---- the remote stage per verdict ----

func TestRemoteStableWaitsForNextRelease(t *testing.T) {
	for _, file := range []string{"update-check-stable-waits.txt", "update-check-stable-uptodate.txt"} {
		t.Run(file, func(t *testing.T) {
			h := newHarness(t)
			h.cfg.Update.Channel = config.ChannelStable
			h.hermesHelp(helpWithChannels)
			h.answer(execx.Result{Output: fixture(t, file)}, "update", "--check", "--channel", "stable")
			var r *Run
			h.wrapRun(func(x *Run) { r = x })
			if err := h.run(); err != nil {
				t.Fatalf("err=%v\n%s", err, h.output())
			}
			if b := r.Facts.Check.Behind; b == nil || *b {
				t.Errorf("nothing to update must read as not behind: %v", b)
			}
			h.nothingClosed(t)
			h.mustContain("Hermes is up to date", "stable v0.22.0", "Nothing to update")
			if strings.Contains(h.output(), "on main") {
				t.Errorf("a release channel must not read 'on main':\n%s", h.output())
			}
			if file == "update-check-stable-waits.txt" {
				h.mustContain("waits for the next release")
			}
		})
	}
}

func TestRemoteStableAvailableProceeds(t *testing.T) {
	h := newHarness(t)
	h.cfg.Update.Channel = config.ChannelStable
	h.hermesHelp(helpWithChannels)
	h.answer(execx.Result{Output: fixture(t, "update-check-stable-available.txt")}, "update", "--check", "--channel", "stable")
	h.stableUpdateStream(t)
	h.con.Keys = []platform.Key{key('y')}
	var r *Run
	h.wrapRun(func(x *Run) { r = x })
	if err := h.run(); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	h.mustContain("release v0.22.1 available", "Hermes release check", "RESULT: OK")
	if strings.Contains(h.output(), "new commit(s) on main") {
		t.Errorf("main wording on a release:\n%s", h.output())
	}
	if got := h.updateArgv(); got != "update --yes --keep-stash --channel stable" {
		t.Errorf("update argv %q", got)
	}
	// what S3 reads from the run facts
	f := r.Facts.Channel
	if !f.Plan.Release || f.Plan.Effective != "stable" || f.Check.Verdict != hermes.VerdictAvailable || f.Check.Label() != "stable v0.22.1" ||
		f.Applied.Channel != "stable" || f.Applied.Label() != "stable v0.22.1" || f.Applied.SwitchSHA != "abcdef0123" || f.Name() != "stable" {
		t.Errorf("facts: %+v", f)
	}
	if r.Facts.Check.PyDeps != nil || r.Facts.Check.NPM != nil || r.Facts.Check.Commits != nil {
		t.Errorf("a release has no prediction yet: %+v", r.Facts.Check)
	}
	if b := r.Facts.Check.Behind; b == nil || !*b {
		t.Errorf("an available release must read as behind: %v", b)
	}
	if r.Facts.Versions != [2]string{"v0.22.0", "v0.22.1"} {
		t.Errorf("versions %v", r.Facts.Versions)
	}
}

// wrapRun hands the Run of the next h.run() to f (to read its facts).
func (h *harness) wrapRun(f func(*Run)) { h.onRun = f }

func TestRemoteStableDoesNotUseTheMainChain(t *testing.T) {
	h := newHarness(t)
	m := &chainMirror{}
	m.st = mirror.Status{OK: true, ConfiguredPath: `X:\m`, JobActive: true}
	h.mirror = m
	h.cfg.Update.Channel = config.ChannelStable
	h.hermesHelp(helpWithChannels)
	h.answer(execx.Result{Output: fixture(t, "update-check-stable-waits.txt")}, "update", "--check", "--channel", "stable")
	if err := h.run(); err != nil {
		t.Fatalf("err=%v", err)
	}
	if m.seeds != 0 || m.compares != 0 {
		t.Errorf("main's mirror seed (%d) / GitHub compare (%d) ran for a release channel", m.seeds, m.compares)
	}
	if got := h.ranCheck(); len(got) != 1 || got[0] != "update --check --channel stable" {
		t.Errorf("checks: %v", got)
	}
}

// chainMirror counts the calls that only make sense for main.
type chainMirror struct {
	fakeMirror
	seeds, compares int
}

func (m *chainMirror) Seed(ctx context.Context, c string) mirror.SeedResult {
	m.seeds++
	return m.fakeMirror.Seed(ctx, c)
}
func (m *chainMirror) APICompare(ctx context.Context, b string) *mirror.Compare {
	m.compares++
	return m.fakeMirror.APICompare(ctx, b)
}

func TestRemoteFollowMainUsesMainChain(t *testing.T) {
	h := newHarness(t)
	n := 3
	m := &chainMirror{}
	m.st = mirror.Status{OK: true, ConfiguredPath: `X:\m`, JobActive: true}
	m.seed = &mirror.SeedResult{Used: true, Reason: "ok", Commits: &n, Complete: true}
	h.mirror = m
	h.channelHome(t, "main", officialURL)
	h.hermesHelp(helpWithChannels)
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	if m.seeds != 1 {
		t.Errorf("mirror seeds: %d", m.seeds)
	}
	h.mustContain("3 new commit(s) on main (local mirror)")
	if got := h.updateArgv(); got != "update --yes --keep-stash --branch main" {
		t.Errorf("update argv %q", got)
	}
	if len(h.ranCheck()) != 0 {
		t.Errorf("the mirror answered; no hermes check expected: %v", h.ranCheck())
	}
}

// stages.go:281 in v0.2.0 ran a bare `--check`, which answers for Hermes'
// own channel (stable on an official checkout) while the update used main.
func TestRemoteMainCheckFallbackPassesBranchMain(t *testing.T) {
	for _, mode := range []string{config.ChannelMain, config.ChannelFollow} {
		h := newHarness(t) // no mirror, no GitHub API: the fallback check decides
		h.cfg.Update.Channel = mode
		h.mainCheck = "Update available: 101 commits behind origin/main."
		h.con.Keys = []platform.Key{key('y')}
		if err := h.run(); err != nil {
			t.Fatalf("%s: err=%v", mode, err)
		}
		if got := h.ranCheck(); len(got) != 1 || got[0] != "update --check --branch main" {
			t.Errorf("%s: checks %v", mode, got)
		}
		h.mustContain("101 new commit(s) on main (git fetch)")
	}
}

func TestRemoteStableUnresolvedStopsExit4(t *testing.T) {
	h := newHarness(t)
	h.cfg.Update.Channel = config.ChannelStable
	h.hermesHelp(helpWithChannels)
	h.answer(execx.Result{Code: 1, Output: fixture(t, "update-check-stable-unresolved.txt")}, "update", "--check", "--channel", "stable")
	h.con.Keys = []platform.Key{key('y')}
	err := h.run()
	wantExit(t, err, apperr.PreflightStop)
	h.nothingClosed(t)
	h.mustContain("could not resolve the stable release", "Connection to api.github.com failed", "Nothing was changed")
	if strings.Contains(h.output(), "Hermes is up to date") {
		t.Errorf("an unresolved release must not read as up to date:\n%s", h.output())
	}
}

func TestRemoteStableOnOldHermesStopsPreflight(t *testing.T) {
	for _, help := range []string{helpLegacy, ""} { // "" = the probe failed
		h := newHarness(t)
		h.cfg.Update.Channel = config.ChannelStable
		if help != "" {
			h.hermesHelp(help)
		}
		err := h.run()
		wantExit(t, err, apperr.PreflightStop)
		h.nothingClosed(t)
		h.mustContain("v0.21.6", "update.channel: main", "Nothing was changed")
		if len(h.ranCheck()) != 0 {
			t.Errorf("no check may run: %v", h.ranCheck())
		}
	}
}

func TestRemoteUnknownWordingStopsExit4(t *testing.T) {
	h := newHarness(t)
	h.cfg.Update.Channel = config.ChannelStable
	h.hermesHelp(helpWithChannels)
	h.answer(execx.Result{Output: "→ Update channel: stable\n✓ All is well in a new wording.\n"}, "update", "--check", "--channel", "stable")
	err := h.run()
	wantExit(t, err, apperr.PreflightStop)
	h.nothingClosed(t)
	h.mustContain("Hermes output changed")
	if strings.Contains(h.output(), "up to date") {
		t.Errorf("unknown wording read as up to date:\n%s", h.output())
	}
}

func TestRemoteFailedReleaseCheckStopsExit4(t *testing.T) {
	h := newHarness(t)
	h.cfg.Update.Channel = config.ChannelStable
	h.hermesHelp(helpWithChannels)
	h.answer(execx.Result{Code: 2, Output: "Traceback: boom"}, "update", "--check", "--channel", "stable")
	err := h.run()
	wantExit(t, err, apperr.PreflightStop)
	h.nothingClosed(t)
	h.mustContain("could not check for updates")
}

// A follow run whose Hermes channel cannot be read offline asks the bare
// check and goes on from the channel it names.
func TestFollowUnresolvedUsesTheChannelOfTheBareCheck(t *testing.T) {
	// the install id cannot be read: Source unknown
	t.Run("stable", func(t *testing.T) {
		h := newHarness(t)
		h.hermesHelp(helpWithChannels)
		h.answer(execx.Result{Code: 2}, "update", "--install-id")
		h.answer(execx.Result{Output: fixture(t, "update-check-stable-available.txt")}, "update", "--check")
		h.stableUpdateStream(t)
		h.con.Keys = []platform.Key{key('y')}
		if err := h.run(); err != nil {
			t.Fatalf("err=%v\n%s", err, h.output())
		}
		if got := h.ranCheck(); len(got) != 1 || got[0] != "update --check" {
			t.Errorf("checks %v (the bare check is reused, not repeated)", got)
		}
		if got := h.updateArgv(); got != "update --yes --keep-stash" {
			t.Errorf("update argv %q", got)
		}
		h.mustContain("release v0.22.1 available")
	})
	t.Run("main", func(t *testing.T) {
		h := newHarness(t)
		h.hermesHelp(helpWithChannels)
		h.answer(execx.Result{Code: 2}, "update", "--install-id")
		h.answer(execx.Result{Output: fixture(t, "update-check-main.txt")}, "update", "--check")
		h.mainCheck = "Update available: 7 commits behind origin/main."
		h.con.Keys = []platform.Key{key('y')}
		if err := h.run(); err != nil {
			t.Fatalf("err=%v\n%s", err, h.output())
		}
		if got := h.updateArgv(); got != "update --yes --keep-stash --branch main" {
			t.Errorf("update argv %q: a main channel is exactly mode main", got)
		}
		h.mustContain("7 new commit(s) on main (git fetch)")
	})
}

// ---- the rollback guard ----

// rollbackHarness is a stable run whose check says the checkout is newer
// than the selected release.
func rollbackHarness(t *testing.T) *harness {
	h := newHarness(t)
	h.cfg.Update.Channel = config.ChannelStable
	h.hermesHelp(helpWithChannels)
	h.answer(execx.Result{Output: versionOnDisk}, "--version")
	h.answer(execx.Result{Output: fixture(t, "update-check-stable-rollback.txt")}, "update", "--check", "--channel", "stable")
	h.stableUpdateStream(t)
	return h
}

func TestRollbackGuardAttendedYes(t *testing.T) {
	h := rollbackHarness(t)
	h.con.Keys = []platform.Key{key('y'), key('y')} // the rollback question, then the usual confirmation
	if err := h.run(); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	h.mustContain("move Hermes BACK from v0.22.0+38.g1f6c02c to stable v0.22.0 (38 commits back)", "rollback confirmed", "RESULT: OK")
	h.mustContain("N to stop (auto-stop in 10 min).")
	if h.updates != 1 || h.updateArgv() != "update --yes --keep-stash --channel stable" {
		t.Errorf("updates=%d argv=%q", h.updates, h.updateArgv())
	}
	if len(h.con.Keys) != 0 {
		t.Errorf("keys left: %v", h.con.Keys)
	}
}

// The prompt counts a short timeout in seconds, a long one in minutes (the
// same words as the final confirmation and the footer under it).
func TestRollbackGuardPromptCountdownWording(t *testing.T) {
	h := rollbackHarness(t)
	h.cfg.Tunables.ConfirmTimeoutS = 90
	h.con.Keys = []platform.Key{key('n')}
	_ = h.run()
	h.mustContain("N to stop (auto-stop in 90s).")
}

func TestRollbackGuardAttendedNo(t *testing.T) {
	for name, keys := range map[string][]platform.Key{
		"n":       {key('n')},
		"enter":   {key('\r')},
		"escape":  {key(0x1b)},
		"timeout": nil,
	} {
		t.Run(name, func(t *testing.T) {
			h := rollbackHarness(t)
			h.cfg.Tunables.ConfirmTimeoutS = 1
			h.con.Keys = keys
			err := h.run()
			wantExit(t, err, apperr.PreflightStop)
			if errors.Is(err, apperr.ErrCancelled) {
				t.Errorf("the refusal is a pre-flight stop (4), not a cancel (3): %v", err)
			}
			h.nothingClosed(t)
			h.mustContain("BACK from", "nothing was changed")
		})
	}
}

// --yes skips the final confirmation, never the question about going back.
func TestRollbackGuardYesFlagDoesNotAnswerIt(t *testing.T) {
	h := rollbackHarness(t)
	h.cfg.Tunables.ConfirmTimeoutS = 1
	err := h.run("yes")
	wantExit(t, err, apperr.PreflightStop)
	h.nothingClosed(t)
}

func TestRollbackGuardUnattended(t *testing.T) {
	h := rollbackHarness(t)
	h.con.Keys = []platform.Key{key('y'), key('y')} // nobody may be asked
	err := h.run("unattended")
	wantExit(t, err, apperr.PreflightStop)
	h.nothingClosed(t)
	h.mustContain("BACK from", "never rolls back", "hermes-safe-update run", "hermes update")
	if len(h.con.Keys) != 2 {
		t.Errorf("an unattended run asked: %v keys left", len(h.con.Keys))
	}
}

func TestRollbackGuardInCheckModeReportsWithoutAsking(t *testing.T) {
	h := rollbackHarness(t)
	h.mode = ModeCheck
	h.con.Keys = []platform.Key{key('y')}
	err := h.run()
	wantExit(t, err, apperr.PreflightStop)
	h.nothingClosed(t)
	h.mustContain("BACK from", "checkout is newer than release stable v0.22.0")
	if len(h.con.Keys) != 1 {
		t.Error("a check asked a question")
	}
}

// The rollback verdict only comes from the check: a waits-for-next-release
// checkout (the default channel, forward-only) is never a rollback.
func TestWaitsForNextReleaseIsNotARollback(t *testing.T) {
	h := newHarness(t)
	h.cfg.Update.Channel = config.ChannelStable
	h.hermesHelp(helpWithChannels)
	h.answer(execx.Result{Output: fixture(t, "update-check-stable-waits.txt")}, "update", "--check", "--channel", "stable")
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.output(), "BACK") || len(h.con.Keys) != 1 {
		t.Errorf("rollback prompt for a waiting checkout:\n%s", h.output())
	}
}

// --force on a waiting checkout still runs Hermes (a no-op there), but a
// rollback is never forced through: it needs the answer above.
func TestForceRunsAWaitingCheckoutButNotARollback(t *testing.T) {
	h := newHarness(t)
	h.cfg.Update.Channel = config.ChannelStable
	h.hermesHelp(helpWithChannels)
	h.answer(execx.Result{Output: fixture(t, "update-check-stable-waits.txt")}, "update", "--check", "--channel", "stable")
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run("force"); err != nil {
		t.Fatal(err)
	}
	if h.updates != 1 {
		t.Errorf("--force did not run the update: %d", h.updates)
	}
	h2 := rollbackHarness(t)
	h2.cfg.Tunables.ConfirmTimeoutS = 1
	wantExit(t, h2.run("force", "unattended"), apperr.PreflightStop)
	h2.nothingClosed(t)
}

// "Did main move while this ran?" says nothing on a release channel (main is
// always ahead of a release), so the Newer row must not look at main there.
func TestNoNewerRowOnAReleaseChannel(t *testing.T) {
	for _, c := range []struct {
		name      string
		mode      string
		wantCalls int
	}{{"stable", config.ChannelStable, 0}, {"main control", config.ChannelMain, 1}} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			m := &fakeMirror{newer: mirror.Newer{Ahead: true}, newerOK: true}
			h.mirror = m
			h.cfg.Update.Channel = c.mode
			h.hermesHelp(helpWithChannels)
			h.answer(execx.Result{Output: fixture(t, "update-check-stable-available.txt")}, "update", "--check", "--channel", "stable")
			h.stableUpdateStream(t)
			h.con.Keys = []platform.Key{key('y')}
			if err := h.run(); err != nil {
				t.Fatalf("err=%v\n%s", err, h.output())
			}
			if m.newerCalls != c.wantCalls {
				t.Errorf("NewerThan called %d times, want %d", m.newerCalls, c.wantCalls)
			}
			if c.wantCalls == 0 && strings.Contains(h.output(), "landed on GitHub") {
				t.Errorf("a Newer row about main on a release channel:\n%s", h.output())
			}
		})
	}
}
