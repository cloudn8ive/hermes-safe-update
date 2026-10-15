package main

import (
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
)

// Hermes v0.21.6/v0.21.7 print "Selected release available" even when the
// release is OLDER than the checkout, and `hermes update --yes` then moves the
// checkout back without asking. The real CLI against the synthetic install:
// git (`merge-base --is-ancestor`) finds it, before anything is closed.

// behindWorld is an explicit stable record on a Hermes that words every
// release "available"; the release commit is an ancestor of HEAD.
func behindWorld(t *testing.T) *e2eWorld {
	t.Helper()
	w := newWorld(t)
	w.record = "stable"
	w.releaseOut = readContract(t, "update-check-stable-available.txt")
	w.applyArgv = []string{"update", "--yes", "--keep-stash"}
	w.update = stableUpdate()
	w.releaseLocal = true
	w.releaseBehind = true
	return w
}

func TestE2EAvailableButBehindIsAGuardedRollback(t *testing.T) {
	t.Run("unattended is refused", func(t *testing.T) {
		w := behindWorld(t)
		before := w.storeBytes()
		w.cli("--plain", "--unattended")
		w.wantCode(apperr.PreflightStop)
		w.wantScreen("move Hermes BACK from v0.21.5 to stable v0.22.1 (12 commits back)", "never rolls back", "hermes-safe-update run", "hermes update")
		w.nothingChanged(before)
		if w.con.prompts != 0 {
			t.Errorf("an unattended run asked %d question(s)", w.con.prompts)
		}
	})
	t.Run("attended N", func(t *testing.T) {
		w := behindWorld(t)
		w.con.script(conStep{key: 'n'})
		before := w.storeBytes()
		w.cli("--plain")
		w.wantCode(apperr.PreflightStop)
		w.wantScreen("BACK from v0.21.5 to stable v0.22.1 (12 commits back)", "not rolled back")
		w.nothingChanged(before)
	})
	t.Run("attended Y", func(t *testing.T) {
		w := behindWorld(t)
		w.con.script(conStep{key: 'y'}, conStep{key: 'y'})
		w.cli("--plain")
		w.wantCode(apperr.OK)
		w.wantScreen("BACK from v0.21.5 to stable v0.22.1 (12 commits back)", "rollback confirmed")
	})
	t.Run("a check reports it and changes nothing", func(t *testing.T) {
		w := behindWorld(t)
		before := w.storeBytes()
		w.cli("--plain", "check")
		w.wantCode(apperr.PreflightStop)
		w.wantScreen("BACK from v0.21.5 to stable v0.22.1 (12 commits back)")
		w.nothingChanged(before)
	})
}

// The release is not in a shallow checkout: git cannot prove the update goes
// forward, so an unattended run does not take the chance.
func TestE2EReleaseNotLocalInAShallowCheckoutIsRefusedUnattended(t *testing.T) {
	w := behindWorld(t)
	w.releaseLocal, w.releaseBehind, w.shallow = false, false, true
	before := w.storeBytes()
	w.cli("--plain", "--unattended")
	w.wantCode(apperr.PreflightStop)
	w.wantScreen("might move Hermes BACK", "cannot rule that out")
	w.nothingChanged(before)
}

// A full clone that lacks the release is a normal forward update.
func TestE2EReleaseNotLocalInAFullCloneGoesForward(t *testing.T) {
	w := behindWorld(t)
	w.releaseLocal, w.releaseBehind = false, false
	w.cli("--plain", "--unattended")
	w.wantCode(apperr.OK)
	w.noScreen("BACK")
}
