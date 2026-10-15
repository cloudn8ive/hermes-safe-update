package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// Truthful verify (v0.4.0) through the real wiring: the tolerated relaunch
// exit, the gateway rescue and Hermes' hold, the double relaunch note, and a
// launcher that the update replaced.

// A pre-v0.21.6 updater: "Update complete!", then its own relaunch check
// failed and the process exited 1 (hermes-agent #123971). Safe Update's own
// look finds the gateway on the new code.
func TestE2ERelaunchUnverifiedExit1WithHealthyGatewayIsOK(t *testing.T) {
	modes(t, func(t *testing.T, vt bool) {
		w := newWorld(t)
		w.vt = vt
		w.update = append(happyUpdate(),
			updateLine{0, "", nil},
			updateLine{0, "  ⚠ Windows gateway restart could not be verified for: default", nil},
			updateLine{0, "    Recover with: hermes gateway restart", nil},
			updateLine{0, "Traceback (most recent call last):", nil},
			updateLine{0, "RuntimeError: Windows gateway relaunch after update was not verified alive: default", nil})
		w.updateRC = 1
		w.con.script(conStep{key: 'y'})
		w.cli()
		w.transcript("relaunch-unverified-healthy")
		w.wantCode(apperr.OK)
		w.wantScreen(
			"RESULT: OK (updater exited 1 on its own unverified gateway relaunch, hermes-agent #123971; gateway verified on the new code by Safe Update)",
			"Hermes is updated and verified.", "Gateway check", "could not be verified for: default")
		w.noScreen("RESULT: PROBLEM")
	})
}

// The same exit 1 with any other failure in the output stays a PROBLEM.
func TestE2ERelaunchUnverifiedWithAnotherFailureStaysProblem(t *testing.T) {
	w := newWorld(t)
	w.update = append(happyUpdate(),
		updateLine{0, "✗ Building the web UI failed: npm exited 1", nil},
		updateLine{0, "RuntimeError: Windows gateway relaunch after update was not verified alive: default", nil})
	w.updateRC = 1
	w.con.script(conStep{key: 'y'})
	w.cli("--plain")
	w.wantCode(apperr.Failure)
	w.wantScreen("RESULT: PROBLEM (update rc=1")
	w.noScreen("#123971")
}

// Hermes paused the gateway and never brought it back (#129171); Safe Update
// did not stop it, but starts it and says so.
func TestE2EGatewayLeftDownIsRescuedAndSaid(t *testing.T) {
	modes(t, func(t *testing.T, vt bool) {
		w := newWorld(t)
		w.vt = vt
		u := happyUpdate()
		for i := range u {
			if u[i].line == "→ Restarting the gateway…" {
				u[i].do = nil // Hermes' own restart never happens
			}
		}
		w.update = u
		w.con.script(conStep{key: 'y'})
		w.cli()
		w.transcript("gateway-left-down-rescued")
		w.wantCode(apperr.OK)
		w.wantScreen("Gateway rescue", "restarted by Safe Update (Hermes left it stopped); down about",
			"RESULT: OK", "Hermes is updated and verified.")
		if !w.ran(w.launcher, "gateway", "start", "--all") {
			t.Error("the gateway was not started")
		}
	})
}

// Hermes left the paused gateway stopped on purpose: Safe Update does not
// start it, and says why.
func TestE2EGatewayHeldByHermesIsNotStarted(t *testing.T) {
	modes(t, func(t *testing.T, vt bool) {
		w := newWorld(t)
		w.vt = vt
		w.update = []updateLine{
			{0, "→ Update channel: main", nil},
			{2 * time.Second, "→ Stopping Windows gateway (pid 8211) so files can be replaced…", (*e2eWorld).removeGateways},
			{21 * time.Second, "→ Fetching updates...", nil},
			{9 * time.Second, "→ Pulling updates...", func(w *e2eWorld) { w.setHead(e2eNew) }},
			{30 * time.Second, "✗ Update did not finish: interrupted", nil},
			{0, "  ⚠ Paused gateway(s) left stopped: the checkout is not whole (pull interrupted).", nil},
			{0, "    The next `hermes` command (or `hermes update`) restarts them.", nil},
		}
		w.updateRC = 1
		w.con.script(conStep{key: 'y'})
		w.cli()
		w.transcript("gateway-held-not-started")
		w.wantCode(apperr.Failure)
		w.wantScreen("RESULT: PROBLEM", "the gateway is not running and was not started: Hermes left it stopped on purpose",
			"the checkout is not whole (pull interrupted)", "Gateway rescue")
		if w.ran(w.launcher, "gateway", "start") {
			t.Error("Safe Update started a gateway Hermes held stopped")
		}
	})
}

// #126821: two "Restarting ... gateway profile(s)" lines naming one profile.
func TestE2EDoubleRelaunchIsANote(t *testing.T) {
	modes(t, func(t *testing.T, vt bool) {
		w := newWorld(t)
		w.vt = vt
		w.update = append(happyUpdate(),
			updateLine{0, "  ✓ Restarting manual gateway profile(s): default", nil},
			updateLine{0, "  ✓ Restarting Windows gateway profile(s): default", nil})
		w.con.script(conStep{key: 'y'})
		w.cli()
		w.transcript("double-relaunch")
		w.wantCode(apperr.OK)
		w.wantScreen("Double launch", "relaunched the gateway for profile default twice (hermes-agent #126821)",
			"RESULT: OK", "Hermes is updated and verified.")
	})
}

// The update replaced bin\hermes.exe (here: it is gone and the checkout's own
// launcher is the one left): everything after the update runs the other one.
func TestE2ELauncherReplacedByTheUpdateIsFollowed(t *testing.T) {
	modes(t, func(t *testing.T, vt bool) {
		w := newWorld(t)
		w.vt = vt
		w.launcher2 = filepath.Join(w.checkout, ".hermes", "bin", "hermes.exe")
		writeFile(t, w.launcher2, "x")
		w.plat.Paths.(*platform.FakePaths).Launchers = []string{w.launcher, w.launcher2}
		u := happyUpdate()
		for i := range u {
			if u[i].line == "✓ Code updated!" {
				u[i].do = func(w *e2eWorld) { _ = os.Remove(w.launcher) }
			}
		}
		w.update = u
		w.con.script(conStep{key: 'y'})
		w.cli()
		w.transcript("launcher-replaced")
		w.wantCode(apperr.OK)
		w.wantScreen("Launcher", "is gone after the update; Safe Update used", "RESULT: OK", "Hermes is updated and verified.")
		calls := w.Calls()
		verify := append([]string{w.launcher2}, hermes.VerifyModuleArgv...)
		if !slices.ContainsFunc(calls, func(c []string) bool { return slices.Equal(c, verify) }) {
			t.Errorf("the desktop verify did not run through the new launcher: %v", calls)
		}
		gone := false
		for _, c := range calls {
			if gone && c[0] == w.launcher {
				t.Errorf("the vanished launcher was run again: %v", c)
			}
			if c[0] == w.launcher && len(c) > 1 && c[1] == "update" && c[2] == "--yes" {
				gone = true // the update itself is the last run of the old path
			}
		}
	})
}
