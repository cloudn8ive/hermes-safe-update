package update

import (
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// ---- S1: HEAD is looked at again before Hermes is closed ----

const headChangedText = "The Hermes checkout changed while Safe Update waited"

// stoppedForMovedHead: exit 4, nothing closed, claimed or run, the text says why.
func stoppedForMovedHead(t *testing.T, h *harness, err error) {
	t.Helper()
	wantExit(t, err, apperr.PreflightStop)
	h.nothingClosed(t)
	if got := h.updateArgv(); got != "" {
		t.Errorf("hermes update ran: %q", got)
	}
	h.mustContain(headChangedText, "no longer the one this run checked", "Nothing was changed")
}

// The confirm prompt is the long wait: HEAD moves while the user thinks.
func TestStableHoldRechecksHeadAfterTheConfirmPrompt(t *testing.T) {
	h := holdHarness(t)
	h.holdReleaseContains() // the decision was "update to the release"
	scripted(h, scriptStep{key: 'y', act: func() { h.repo.set(newSHA) }})
	stoppedForMovedHead(t, h, h.run())
}

// No prompt at all: the wait for idle sessions is the window.
func TestStableHoldRechecksHeadAfterTheIdleWait(t *testing.T) {
	h := holdHarness(t)
	h.holdReleaseContains()
	h.sessions.script = [][]hermes.Session{busy("s1"), nil}
	h.sessions.onCall = func(n int) {
		if n == 2 { // idle again: the moment the run would go on
			h.repo.set(newSHA)
		}
	}
	stoppedForMovedHead(t, h, h.run("unattended"))
	if h.sessions.calls < 2 {
		t.Errorf("the idle wait did not happen (session reads %d)", h.sessions.calls)
	}
}

// A HEAD that cannot be read is not "unchanged".
func TestStableHoldHeadThatCannotBeReadStops(t *testing.T) {
	h := holdHarness(t)
	h.holdReleaseContains()
	scripted(h, scriptStep{key: 'y', act: func() { h.repo.headErr = true }})
	err := h.run()
	wantExit(t, err, apperr.PreflightStop)
	h.nothingClosed(t)
	h.mustContain(headChangedText, "could not be read")
}

// The same HEAD after the same wait goes on, with the release argv.
func TestStableHoldUnchangedHeadAfterTheWaitGoesOn(t *testing.T) {
	h := holdHarness(t)
	h.holdReleaseContains()
	scripted(h, scriptStep{key: 'y'})
	if err := h.run(); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	if h.updates != 1 || h.updateArgv() != "update --yes --keep-stash --channel stable" {
		t.Errorf("updates=%d argv=%q", h.updates, h.updateArgv())
	}
	if strings.Contains(h.output(), headChangedText) {
		t.Errorf("stopped for a HEAD that did not move:\n%s", h.output())
	}
}

// The rollback guard (update.older_release: ask) decided from the same HEAD,
// so a release channel in ask mode is re-checked too.
func TestRollbackGuardAskRechecksHeadToo(t *testing.T) {
	h := holdHarness(t)
	h.cfg.Update.OlderRelease = config.OlderReleaseAsk
	h.holdReleaseContains()
	scripted(h, scriptStep{key: 'y', act: func() { h.repo.set(newSHA) }})
	stoppedForMovedHead(t, h, h.run())
}

// Main has no release argv that could move Hermes back: not re-checked.
func TestHeadMovingDuringTheWaitIsNotAStopOnMain(t *testing.T) {
	h := newHarness(t)
	h.cfg.Update.Channel = config.ChannelMain
	h.cfg.Tunables.ConfirmTimeoutS = 3
	scripted(h, scriptStep{key: 'y', act: func() { h.repo.set(newSHA) }})
	if err := h.run(); err != nil {
		t.Fatalf("err=%v\n%s", err, h.output())
	}
	if h.updates != 1 {
		t.Errorf("updates=%d", h.updates)
	}
}

// Without a git checkout to read, or when the installed commit was never
// known (update.older_release: ask, where the run can still go on), there is
// nothing to compare: the re-check does not invent a stop.
func TestHeadRecheckNeedsAKnownHeadToCompare(t *testing.T) {
	t.Run("no git", func(t *testing.T) {
		h := holdHarness(t)
		h.cfg.Update.OlderRelease = config.OlderReleaseAsk
		h.wrapRun(func(x *Run) { x.Repo = nil })
		h.con.Keys = []platform.Key{key('y')}
		// the fakes cannot verify an update without a readable HEAD, so the
		// exit code is not the point: the update ran and nothing stopped it
		err := h.run()
		if h.updates != 1 || apperr.ExitCode(err) == apperr.PreflightStop {
			t.Fatalf("err=%v updates=%d\n%s", err, h.updates, h.output())
		}
	})
	t.Run("the installed commit was never known", func(t *testing.T) {
		h := holdHarness(t)
		h.cfg.Update.OlderRelease = config.OlderReleaseAsk
		h.repo.headErr = true
		h.con.Keys = []platform.Key{key('y'), key('y')} // the guard cannot rule a rollback out and asks, then the confirmation
		// the fakes cannot verify an update without a readable HEAD, so the
		// exit code is not the point: the update ran and nothing stopped it
		err := h.run()
		if h.updates != 1 || apperr.ExitCode(err) == apperr.PreflightStop {
			t.Fatalf("err=%v updates=%d\n%s", err, h.updates, h.output())
		}
		if strings.Contains(h.output(), headChangedText) {
			t.Errorf("stopped for a HEAD that was never known:\n%s", h.output())
		}
	})
}
