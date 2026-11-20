package main

import (
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
)

// The stable hold (update.older_release: wait, the default), end to end with
// the real CLI against the synthetic install. holdWorld is an explicit stable
// record on a checkout that is newer than the latest release: `hermes update`
// would move it back with no question.
func holdWorld(t *testing.T) *e2eWorld {
	t.Helper()
	w := newWorld(t)
	w.record = "stable"
	w.releaseOut = rollbackCheck
	w.applyArgv = []string{"update", "--yes", "--keep-stash"}
	w.update = stableUpdate()
	return w
}

const holdE2EText = "Waiting for stable: this build (v0.21.5) is newer than stable v0.21.4; Safe Update stays on it until a stable release includes it."

func TestE2EHoldWaitsInsteadOfRollingBack(t *testing.T) {
	t.Run("attended run", func(t *testing.T) {
		modes(t, func(t *testing.T, vt bool) {
			w := holdWorld(t)
			w.vt = vt
			before := w.storeBytes()
			if vt {
				w.cli()
			} else {
				w.cli("--plain")
			}
			w.transcript("hold-wait")
			w.wantCode(apperr.OK)
			w.wantScreen(holdE2EText, "update.older_release: ask", "--allow-rollback")
			w.noScreen("Press", "STOP", "BACK", "Hermes is updated")
			w.nothingChanged(before)
			if w.con.prompts != 0 {
				t.Errorf("asked %d question(s)", w.con.prompts)
			}
			if w.Head() != e2eOld {
				t.Errorf("head moved to %s", w.Head())
			}
		})
	})
	t.Run("unattended", func(t *testing.T) {
		w := holdWorld(t)
		before := w.storeBytes()
		w.cli("--plain", "--unattended")
		w.wantCode(apperr.OK)
		w.wantScreen(holdE2EText)
		w.nothingChanged(before)
	})
	t.Run("check", func(t *testing.T) {
		w := holdWorld(t)
		before := w.storeBytes()
		w.cli("--plain", "check")
		w.transcript("hold-wait-check")
		w.wantCode(apperr.OK)
		w.wantScreen(holdE2EText)
		w.nothingChanged(before)
	})
}

// A release that cannot be proven to contain this build also waits.
func TestE2EHoldUnprovenWaits(t *testing.T) {
	w := holdWorld(t)
	w.releaseOut = readContract(t, "update-check-stable-available.txt")
	w.shallow = true
	before := w.storeBytes()
	w.cli("--plain", "--unattended")
	w.transcript("hold-unproven")
	w.wantCode(apperr.OK)
	w.wantScreen("Waiting for stable: Safe Update could not confirm that stable v0.22.1 contains this build (v0.21.5)")
	w.nothingChanged(before)
}

// Diverged: neither contains the other.
func TestE2EHoldDivergedWaits(t *testing.T) {
	w := holdWorld(t)
	w.releaseOut = readContract(t, "update-check-stable-available.txt")
	w.releaseLocal, w.releaseDiverged = true, true
	before := w.storeBytes()
	w.cli("--plain", "--unattended")
	w.wantCode(apperr.OK)
	w.wantScreen("Waiting for stable: stable v0.22.1 does not contain this build (v0.21.5), and this build does not contain it")
	w.nothingChanged(before)
}

// A repair, --force and finishing an unfinished update would all run
// `hermes update` for the stable channel: refused while waiting, nothing changed.
func TestE2EHoldRefusesWhatWouldRollBack(t *testing.T) {
	t.Run("repair", func(t *testing.T) {
		w := holdWorld(t)
		before := w.storeBytes()
		w.cli("--plain", "repair")
		w.wantCode(apperr.PreflightStop)
		w.wantScreen("Repair is not available while Safe Update waits for stable", "repair --channel main")
		w.nothingChanged(before)
	})
	t.Run("force", func(t *testing.T) {
		w := holdWorld(t)
		before := w.storeBytes()
		w.cli("--plain", "--force")
		w.wantCode(apperr.PreflightStop)
		w.wantScreen("--force is not available while Safe Update waits for stable")
		w.nothingChanged(before)
	})
	t.Run("an unfinished update", func(t *testing.T) {
		w := holdWorld(t)
		w.writeReceipt(killedReceipt)
		before := w.storeBytes()
		w.cli("--plain", "--unattended")
		w.wantCode(apperr.PreflightStop)
		w.wantScreen("The last update did not finish, but it cannot be finished while Safe Update waits for stable")
		w.nothingChanged(before)
	})
}

// --allow-rollback is update.older_release: ask for one run: today's question.
func TestE2EAllowRollbackFlagAsksAsBefore(t *testing.T) {
	t.Run("unattended still refuses", func(t *testing.T) {
		w := holdWorld(t)
		before := w.storeBytes()
		w.cli("--plain", "--unattended", "--allow-rollback")
		w.wantCode(apperr.PreflightStop)
		w.wantScreen("move Hermes BACK from v0.21.5 to stable v0.21.4", "never rolls back")
		w.noScreen("Waiting for stable")
		w.nothingChanged(before)
	})
	t.Run("attended Y rolls back", func(t *testing.T) {
		w := holdWorld(t)
		w.con.script(conStep{key: 'y'}, conStep{key: 'y'})
		w.cli("--plain", "--allow-rollback")
		w.wantCode(apperr.OK)
		w.wantScreen("BACK from v0.21.5 to stable v0.21.4", "rollback confirmed", "Hermes is updated and verified.")
	})
}

// `channel` explains the wait state when it applies (stable) and is silent on
// main.
func TestE2EChannelShowExplainsTheHold(t *testing.T) {
	t.Run("stable: wait", func(t *testing.T) {
		w := newWorld(t)
		w.record = "stable"
		w.cli("channel")
		w.wantCode(apperr.OK)
		w.wantOut("Older release:       wait (update.older_release).", "never moves Hermes back", "update.older_release: ask", "--allow-rollback")
	})
	t.Run("stable: ask", func(t *testing.T) {
		w := newWorld(t)
		w.record = "stable"
		w.askBeforeRollback()
		w.cli("channel")
		w.wantCode(apperr.OK)
		w.wantOut("Older release:       ask (update.older_release).", "asks before moving Hermes back")
		w.noOut("Older release:       wait")
	})
	t.Run("main: nothing to explain", func(t *testing.T) {
		w := newWorld(t) // record main
		w.cli("channel")
		w.wantCode(apperr.OK)
		w.noOut("Older release")
	})
	t.Run("a forced stable channel", func(t *testing.T) {
		w := newWorld(t)
		w.yaml += "update:\n  channel: stable\n"
		w.cli("channel")
		w.wantCode(apperr.OK)
		w.wantOut("Older release:       wait")
	})
}

// --allow-rollback only belongs to the update commands.
func TestAllowRollbackFlagParsing(t *testing.T) {
	for _, args := range [][]string{{"--allow-rollback"}, {"check", "--allow-rollback"}, {"repair", "--allow-rollback"}} {
		o, err := parseArgs(args)
		if err != nil || !o.allowRoll {
			t.Errorf("%v: allowRoll=%v err=%v", args, o.allowRoll, err)
		}
	}
	for _, args := range [][]string{{"channel", "--allow-rollback"}, {"list-backups", "--allow-rollback"}} {
		if _, err := parseArgs(args); err == nil {
			t.Errorf("%v: accepted", args)
		}
	}
}
