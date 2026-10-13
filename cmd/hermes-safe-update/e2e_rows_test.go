package main

import (
	"slices"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
)

// End-to-end scenarios for the summary rows that explain the run: the commits
// that were really installed, newer commits that landed meanwhile, and what
// happened to the desktop app (desktop.relaunch).

const newerTail = "landed on GitHub while this ran; run Safe Update again when convenient"

func TestE2ENewerCommitsLandedWhileTheUpdateRan(t *testing.T) {
	modes(t, func(t *testing.T, vt bool) {
		w := newWorld(t)
		w.vt = vt
		w.installed = 5 // the pre-flight forecast said 3; main moved before `hermes update` fetched
		w.newerMode, w.newerN = "ahead", 7
		w.con.script(conStep{key: 'y'})
		w.cli()
		w.transcript("newer-landed")
		w.wantCode(apperr.OK)
		w.wantScreen("Hermes is updated and verified.", "3 new commit(s) on main (GitHub API)",
			"v0.21.5 → v0.21.6  (5 commits)", "7 more commits "+newerTail)
		w.noScreen("(3 commits)", "WARN")
	})
}

func TestE2ENewerCommitsFallBackToLsRemoteWithoutANumber(t *testing.T) {
	w := newWorld(t) // the API answers nothing for the new HEAD
	w.lsRemoteTip = "9999999999999999999999999999999999999999"
	w.con.script(conStep{key: 'y'})
	w.cli()
	w.wantCode(apperr.OK)
	w.wantScreen("Newer commits " + newerTail)
	if !w.ran(w.git, "-c", "gc.auto=0", "-c", "maintenance.auto=false", "ls-remote") {
		t.Errorf("ls-remote not asked: %v", w.Calls())
	}
}

func TestE2ENewerLookFindsNothingAndSaysNothing(t *testing.T) {
	for _, mode := range []string{"", "identical"} { // API down -> ls-remote says main did not move
		w := newWorld(t)
		w.newerMode = mode
		w.con.script(conStep{key: 'y'})
		w.cli()
		w.wantCode(apperr.OK)
		w.noScreen("landed on GitHub", "Newer ")
	}
}

func TestE2ENewerLookNeverRunsInACheck(t *testing.T) {
	w := newWorld(t)
	w.newerMode, w.newerN = "ahead", 7
	w.cli("--check")
	w.noScreen("landed on GitHub")
	if w.ran(w.git, "-c", "gc.auto=0", "-c", "maintenance.auto=false", "ls-remote") {
		t.Error("--check asked GitHub for newer commits")
	}
}

func TestE2EVersionCountFallsBackWhenGitCannotCount(t *testing.T) {
	w := newWorld(t)
	w.installed = -1
	w.update = slices.Clone(w.update)
	w.update = append([]updateLine{{0, "→ Found 8 new commit(s)", nil}}, w.update...)
	w.con.script(conStep{key: 'y'})
	w.cli()
	w.wantCode(apperr.OK)
	w.wantScreen("v0.21.5 → v0.21.6  (8 commits)")
}

func TestE2EDesktopRelaunchAlwaysReopensAClosedApp(t *testing.T) {
	modes(t, func(t *testing.T, vt bool) {
		w := newWorld(t)
		w.vt = vt
		w.yaml += "desktop:\n  relaunch: \"always\"\n"
		w.procs.Remove(pidDesktop) // the user closed Hermes a moment before
		w.con.script(conStep{key: 'y'})
		w.cli()
		w.transcript("relaunch-always-closed-app")
		w.wantCode(apperr.OK)
		w.wantScreen("until Hermes reopened", "relaunched Hermes desktop")
		w.noScreen("not reopened")
		if c := w.app.Calls(); !slices.Equal(c, []string{"launch Hermes.exe"}) {
			t.Errorf("app calls %v", c)
		}
	})
}

func TestE2EDesktopClosedAtStartStaysClosedByDefaultAndSaysWhy(t *testing.T) {
	modes(t, func(t *testing.T, vt bool) {
		w := newWorld(t)
		w.vt = vt
		w.procs.Remove(pidDesktop)
		w.con.script(conStep{key: 'y'})
		w.cli()
		w.transcript("app-not-reopened")
		w.wantCode(apperr.OK)
		w.wantScreen("not reopened (it was closed when the update started; set desktop.relaunch: always to reopen it)")
		if c := w.app.Calls(); len(c) != 0 {
			t.Errorf("app calls %v", c)
		}
	})
}

func TestE2EDesktopRelaunchNeverLeavesAnOpenAppClosed(t *testing.T) {
	w := newWorld(t)
	w.yaml += "desktop:\n  relaunch: \"never\"\n"
	w.con.script(conStep{key: 'y'})
	w.cli()
	w.wantCode(apperr.OK)
	w.wantScreen(`not reopened (desktop.relaunch is "never")`)
	if c := w.app.Calls(); !slices.Equal(c, []string{"close 4242"}) {
		t.Errorf("app calls %v", c)
	}
}

func TestE2EDesktopRelaunchBadValueIsAConfigError(t *testing.T) {
	w := newWorld(t)
	w.yaml += "desktop:\n  relaunch: \"sometimes\"\n"
	w.cli()
	w.wantCode(apperr.Usage)
	w.wantScreen("invalid configuration", "safe-update.yaml:4: desktop.relaunch: must be one of auto, always, never")
	if c := w.app.Calls(); len(c) != 0 {
		t.Errorf("app calls %v", c)
	}
}
