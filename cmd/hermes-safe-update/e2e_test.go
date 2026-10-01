package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// Scenario tests of the whole `run` / `check` flow through the real CLI
// wiring (see e2e_harness_test.go). Each writes a transcript to
// testdata/replay/ (.txt plain, .ansi VT at 100 columns); run with
// -update to accept changes and review the diff.

func modes(t *testing.T, f func(t *testing.T, vt bool)) {
	for _, vt := range []bool{false, true} {
		name := "plain"
		if vt {
			name = "vt"
		}
		t.Run(name, func(t *testing.T) { f(t, vt) })
	}
}

// nothingChanged asserts that no mutating effect happened: no app
// close/launch, no kill, no `hermes update`, no gateway stop/start, no
// settings write, no GC, no hook, the HEAD and the settings store as before,
// and no update marker left.
func (w *e2eWorld) nothingChanged(store []byte) {
	w.t.Helper()
	if c := w.app.Calls(); len(c) != 0 {
		w.t.Errorf("app calls %v", c)
	}
	if k := w.procs.Killed(); len(k) != 0 {
		w.t.Errorf("killed %v", k)
	}
	if m := w.Mutations(); len(m) != 0 {
		w.t.Errorf("mutations %v", m)
	}
	if h := w.Head(); h != e2eOld {
		w.t.Errorf("head moved to %s", h)
	}
	if store != nil {
		if b, _ := os.ReadFile(w.storePath(w.userData)); !bytes.Equal(b, store) {
			w.t.Error("settings store changed")
		}
	}
	if _, err := os.Stat(filepath.Join(w.home, hermes.MarkerFile)); err == nil {
		w.t.Error("update marker left behind")
	}
}

func (w *e2eWorld) storeBytes() []byte {
	b, err := os.ReadFile(w.storePath(w.userData))
	if err != nil {
		w.t.Fatal(err)
	}
	return b
}

func TestE2EHappyPath(t *testing.T) {
	modes(t, func(t *testing.T, vt bool) {
		w := newWorld(t)
		w.vt = vt
		w.con.script(conStep{key: 'y'})
		w.cli()
		w.transcript("happy-path")
		w.wantCode(apperr.OK)
		w.wantScreen("Hermes is updated and verified.", "3 new commit(s) on main (GitHub API)",
			"node packages", "changed, will be reinstalled (slower build)",
			"running the new code "+e2eNew[:10], "v0.21.5 → v0.21.6", "relaunched Hermes desktop",
			"removed 2 old dependency set(s), freed 612 MB")
		w.noScreen("computer-use install --upgrade", "WARN")
		if got := w.Head(); got != e2eNew {
			t.Errorf("head %s", got)
		}
		if c := w.app.Calls(); !slices.Equal(c, []string{"close 4242", "launch Hermes.exe"}) {
			t.Errorf("app calls %v", c)
		}
		if k := w.procs.Killed(); !slices.Equal(k, []int{pidBackend}) {
			t.Errorf("killed %v (only the leftover backend)", k)
		}
		if _, err := os.Stat(filepath.Join(w.home, hermes.MarkerFile)); err == nil {
			t.Error("marker not released")
		}
		if _, err := os.Stat(filepath.Join(w.home, "logs", "safe-update-timings.json")); err != nil {
			t.Errorf("timings not saved: %v", err)
		}
	})
}

func TestE2ENoUpdateAvailable(t *testing.T) {
	modes(t, func(t *testing.T, vt bool) {
		w := newWorld(t)
		w.vt = vt
		w.apiStatus = "identical"
		before := w.storeBytes()
		w.cli()
		w.transcript("no-update")
		w.wantCode(apperr.OK)
		w.wantScreen("none, already up to date (GitHub API)", "Nothing to update", "Hermes is up to date")
		w.nothingChanged(before)
	})
}

func TestE2EUpdateCheckFallsBackToGitFetch(t *testing.T) {
	w := newWorld(t)
	w.apiStatus = "down"
	w.checkOut = "→ Fetching from origin...\n⚕ Update available: 3 commits behind origin/main.\n"
	w.con.script(conStep{key: 'y'})
	w.cli("--plain")
	w.wantCode(apperr.OK)
	w.wantScreen("GitHub API unavailable; checking with git fetch", "3 new commit(s) on main (git fetch)", "GitHub (git fetch)")
}

func TestE2EBusySessionWaitsThenProceeds(t *testing.T) {
	modes(t, func(t *testing.T, vt bool) {
		w := newWorld(t)
		w.vt = vt
		w.busySession()
		w.con.script(
			conStep{timeout: true},                               // still busy after a minute
			conStep{timeout: true, then: (*e2eWorld).endSession}, // the turn finishes
			conStep{key: 'y'},                                    // confirm
		)
		w.cli()
		w.transcript("busy-wait-idle")
		w.wantCode(apperr.OK)
		w.wantScreen("1 active in the last 3 min", "Planning the week", "Press A to abort, F to close Hermes anyway",
			"Hermes is updated and verified.")
		if strings.Count(w.screen(), "Planning the week") < 2 {
			t.Error("the busy session should be listed on each check")
		}
	})
}

func TestE2EBusyUnattendedGivesUpAfter30Min(t *testing.T) {
	w := newWorld(t)
	w.busySession()
	w.onSleep = (*e2eWorld).touchSession // the user keeps working
	before := w.storeBytes()
	start := w.clock.Now()
	w.cli("--plain", "--unattended")
	w.transcript("busy-unattended-cap")
	w.wantCode(apperr.Cancelled)
	w.wantScreen("sessions still active after 30 min; nothing was changed")
	if took := w.clock.Now().Sub(start); took < 30*time.Minute || took > 33*time.Minute {
		t.Errorf("gave up after %v, want 30 min", took)
	}
	w.nothingChanged(before)
}

func TestE2EDesktopIgnoresCloseIsForcedAfter30s(t *testing.T) {
	modes(t, func(t *testing.T, vt bool) {
		w := newWorld(t)
		w.vt = vt
		w.app.ignoreClose = true
		w.app.blocker = "a dialog was open: Unsaved draft"
		w.con.script(conStep{key: 'y'})
		var forcedAt time.Time
		w.app.onForce = func() { forcedAt = w.clock.Now() }
		w.cli()
		w.transcript("close-forced")
		w.wantCode(apperr.OK)
		w.wantScreen("desktop pid 4242 did not close gracefully; forcing", "a dialog was open: Unsaved draft",
			"Hermes is updated and verified.")
		if c := w.app.Calls(); !slices.Equal(c, []string{"close 4242", "force 4242", "launch Hermes.exe"}) {
			t.Errorf("app calls %v", c)
		}
		if d := forcedAt.Sub(w.app.closedAt); d < 30*time.Second || d > 33*time.Second {
			t.Errorf("forced %v after the close request, want 30 s", d)
		}
	})
}

func TestE2ECloseFailsAbortsWithoutUpdating(t *testing.T) {
	w := newWorld(t)
	w.app.ignoreClose, w.app.forceFails = true, true
	w.con.script(conStep{key: 'y'})
	before := w.storeBytes()
	w.cli("--plain")
	w.transcript("close-fails")
	w.wantCode(apperr.Cancelled)
	w.wantScreen("STOP desktop app did not exit")
	if w.ran(append([]string{w.launcher}, hermes.UpdateArgv...)...) {
		t.Error("hermes update ran")
	}
	if m := w.Mutations(); len(m) != 0 {
		t.Errorf("mutations %v", m)
	}
	if w.Head() != e2eOld || !bytes.Equal(w.storeBytes(), before) {
		t.Error("something changed")
	}
	if !w.procs.Alive(pidDesktop) || !w.procs.Alive(pidGateway) {
		t.Error("Hermes should be left running")
	}
}

func TestE2EUpdateFailsRelaunchesAndReportsProblem(t *testing.T) {
	modes(t, func(t *testing.T, vt bool) {
		w := newWorld(t)
		w.vt = vt
		w.update = []updateLine{
			{0, "→ Update channel: main", nil},
			{3 * time.Second, "→ Fetching updates...", nil},
			{40 * time.Second, "fatal: unable to access 'https://github.com/NousResearch/hermes-agent.git/': Could not resolve host", nil},
			{0, "✗ Update failed: git fetch exited 128", nil},
		}
		w.updateRC = 1
		w.con.script(conStep{key: 'y'})
		w.cli()
		w.transcript("update-fails")
		w.wantCode(apperr.Failure)
		w.wantScreen("PROBLEM (update rc=1", "The update needs attention",
			"post-update cleanup and cua-driver refresh skipped: update not verified")
		if c := w.app.Calls(); !slices.Contains(c, "launch Hermes.exe") {
			t.Errorf("not relaunched: %v", c)
		}
		if slices.Contains(w.Mutations(), "gc") || slices.Contains(w.Mutations(), "settings write (copy)") {
			t.Errorf("post-steps ran after a failed update: %v", w.Mutations())
		}
	})
}

func TestE2EUpdateTimeoutKillsTheTree(t *testing.T) {
	w := newWorld(t)
	w.update = happyUpdate()[:6] // hangs in the pydeps step
	w.updateHang = true
	w.con.script(conStep{key: 'y'})
	w.cli("--plain")
	w.transcript("update-timeout")
	w.wantCode(apperr.Failure)
	w.wantScreen("STOP update exceeded 90 min; it was stopped", "RESULT: PROBLEM (update rc=124")
	if k := w.procs.Killed(); !slices.Contains(k, pidUpdate) {
		t.Errorf("update tree not killed: %v", k)
	}
	if w.procs.Alive(pidUpdate) || w.procs.Alive(pidUpdateCh) {
		t.Error("an update process survived the timeout")
	}
	if !slices.Contains(w.app.Calls(), "launch Hermes.exe") {
		t.Error("not relaunched after the timeout")
	}
}

const hooksYAML = `mirror:
  offer: false
hooks:
  - name: "tiles"
    when: "post-update"
    run: ["hermes-tiles.exe", "--refresh"]
    timeout_s: 120
  - name: "notify"
    when: "post-relaunch"
    run: ["hermes-tiles.exe", "--notify"]
    timeout_s: 120
`

func TestE2EVerifyFailsStaysProblemPostStepsWarnOnly(t *testing.T) {
	modes(t, func(t *testing.T, vt bool) {
		w := newWorld(t)
		w.vt = vt
		w.yaml = hooksYAML
		w.staleGW = true // the restarted gateway still reports the old code
		w.con.script(conStep{key: 'y'})
		w.cli()
		w.transcript("verify-fails")
		w.wantCode(apperr.Failure)
		w.wantScreen("code "+e2eOld[:10]+", expected "+e2eNew[:10], "RESULT: PROBLEM (update rc=0, gateway on new code=false",
			"The update needs attention")
		// post-update work is skipped; the post-relaunch hook still runs and cannot flip the result
		if m := w.Mutations(); slices.Contains(m, "gc") || slices.Contains(m, "settings write (copy)") || slices.Contains(m, "hook post-update") {
			t.Errorf("post-update steps ran on an unverified update: %v", m)
		}
		if !slices.Contains(w.Mutations(), "hook post-relaunch") {
			t.Error("post-relaunch hook did not run")
		}
		for _, env := range w.hookEnv {
			if env["HERMES_SAFE_UPDATE_RESULT"] != "problem" {
				t.Errorf("hook env %v", env)
			}
		}
	})
}

func TestE2EKnownFalseFleetWarningIsOK(t *testing.T) {
	w := newWorld(t)
	w.update = append(happyUpdate(), updateLine{0, "✗ " + hermes.KnownFalseFleet + " (fleet health check skipped)", nil})
	w.updateRC = 1
	w.con.script(conStep{key: 'y'})
	w.cli("--plain")
	w.transcript("false-fleet-warning")
	w.wantCode(apperr.OK)
	w.wantScreen("hermes update exit code: 1", "RESULT: OK (updater exited 1 on the known false fleet warning", "Hermes is updated and verified.")
}

func TestE2EStartedFromHermesChatStops(t *testing.T) {
	w := newWorld(t)
	// the updater runs in a shell started by the gateway (a Hermes chat turn)
	w.procs.Add(platform.Process{PID: 7001, PPID: pidGateway, Name: "cmd.exe"})
	w.procs.Add(platform.Process{PID: 7002, PPID: 7001, Name: "hermes-safe-update.exe"})
	w.selfPID = 7002
	w.cli("--plain")
	w.transcript("started-from-chat")
	w.wantCode(apperr.PreflightStop)
	w.wantScreen("Run this from a normal terminal or the Start menu, not from inside a Hermes chat")
	if w.ran(append([]string{w.launcher}, hermes.UpdateArgv...)...) || len(w.app.Calls()) != 0 {
		t.Error("changed something")
	}
}

func TestE2EPauseGateRefusesNothingUpdated(t *testing.T) {
	w := newWorld(t)
	w.probes = []string{"blocked"} // blocked before and after stopping the gateway
	w.con.script(conStep{key: 'y'})
	w.cli("--plain")
	w.transcript("pause-gate-refuses")
	w.wantCode(apperr.Failure)
	w.wantScreen("gateway pause check: BLOCKED", "the update would still be blocked after stopping the gateway",
		"aborted before `hermes update` ran; nothing was changed", "blocked: the gateway could not be stopped cleanly; nothing changed")
	if w.ran(append([]string{w.launcher}, hermes.UpdateArgv...)...) {
		t.Error("hermes update ran")
	}
	if w.Head() != e2eOld {
		t.Error("head moved")
	}
	ps, _ := w.procs.List(t.Context())
	gw := 0
	for _, p := range ps {
		if strings.Contains(p.Cmdline, " gateway run") {
			gw++
		}
	}
	if gw != 1 {
		t.Errorf("gateway not running again (%d)", gw)
	}
	if !slices.Contains(w.app.Calls(), "launch Hermes.exe") {
		t.Error("desktop not reopened")
	}
}

func TestE2EPauseGateClearsAfterStoppingGateway(t *testing.T) {
	w := newWorld(t)
	w.probes = []string{"blocked", "ok"}
	w.con.script(conStep{key: 'y'})
	w.cli("--plain")
	w.wantCode(apperr.OK)
	w.wantScreen("gateway pause gate: clear", "stopped before the update (its lock could not be mapped), restarted after",
		"Hermes is updated and verified.")
}

func TestE2ESettingsMigrationMergesIntoTarget(t *testing.T) {
	w := newWorld(t)
	w.con.script(conStep{key: 'y'})
	w.cli("--plain")
	w.wantCode(apperr.OK)
	w.wantScreen("migrated file:// -> " + targetOrigin)
	tg := w.store()[targetOrigin]
	for k, want := range map[string]string{
		"hermes.desktop.composer.sendOnEnter": `true`,   // only in the old origin: copied
		"hermes.desktop.theme":                `"dark"`, // plain pref, first migration: old wins
		"hermes.desktop.tips.next.v1":         `1`,      // keep-new
	} {
		if tg[k] != want {
			t.Errorf("target %s = %q, want %q", k, tg[k], want)
		}
	}
	bs, _ := filepath.Glob(filepath.Join(w.home, "backups", "settings-*", "manifest.json"))
	if len(bs) != 1 {
		t.Errorf("backups %v", bs)
	}
}

func TestE2ESettingsNothingToMigrate(t *testing.T) {
	w := newWorld(t)
	w.writeStore(map[string]map[string]string{targetOrigin: {"hermes.desktop.theme": `"dark"`}})
	before := w.storeBytes()
	w.con.script(conStep{key: 'y'})
	w.cli("--plain")
	w.transcript("settings-nothing")
	w.wantCode(apperr.OK)
	w.wantScreen("nothing to migrate (only " + targetOrigin + " holds keys)")
	if !bytes.Equal(w.storeBytes(), before) {
		t.Error("store changed")
	}
	if bs, _ := filepath.Glob(filepath.Join(w.home, "backups", "settings-*")); len(bs) != 0 {
		t.Errorf("a no-op run took a backup: %v", bs)
	}
}

func TestE2ESettingsMigratorFailsRollsBackWithWarn(t *testing.T) {
	modes(t, func(t *testing.T, vt bool) {
		w := newWorld(t)
		w.vt = vt
		w.failDumpAt = 4 // the live verify after the swap
		before := w.storeBytes()
		w.con.script(conStep{key: 'y'})
		w.cli()
		w.transcript("settings-rollback")
		w.wantCode(apperr.OK) // the update itself is fine
		w.wantScreen("desktop settings migration failed", "rolled back, live settings unchanged",
			"Hermes is updated and verified.", "Warnings")
		if !bytes.Equal(w.storeBytes(), before) {
			t.Error("live store not rolled back byte-identical")
		}
		left, _ := filepath.Glob(filepath.Join(w.userData, ".hsu-*"))
		if len(left) != 0 {
			t.Errorf("work dirs left in userData: %v", left)
		}
	})
}

func TestE2ESettingsMigratorWriteFailsLeavesStore(t *testing.T) {
	w := newWorld(t)
	w.migrateFail = "write"
	before := w.storeBytes()
	w.con.script(conStep{key: 'y'})
	w.cli("--plain")
	w.wantCode(apperr.OK)
	w.wantScreen("WARN desktop settings migration failed", "live settings unchanged")
	if !bytes.Equal(w.storeBytes(), before) {
		t.Error("live store changed")
	}
}

func TestE2EHookTimeoutIsAWarnRow(t *testing.T) {
	modes(t, func(t *testing.T, vt bool) {
		w := newWorld(t)
		w.vt = vt
		w.yaml = hooksYAML
		w.hookTimeout = true
		w.con.script(conStep{key: 'y'})
		w.cli()
		w.transcript("hook-timeout")
		w.wantCode(apperr.OK)
		w.wantScreen("hook tiles timed out after 2:00", "hook notify timed out", "Hermes is updated and verified.")
		for _, env := range w.hookEnv {
			if env["HERMES_SAFE_UPDATE_RESULT"] != "verified" || env["HERMES_SAFE_UPDATE_STAGE"] == "" {
				t.Errorf("hook env %v", env)
			}
		}
	})
}

func TestE2EPackagedInstallStepsAside(t *testing.T) {
	w := newWorld(t)
	if err := os.RemoveAll(filepath.Join(w.checkout, ".git")); err != nil {
		t.Fatal(err)
	}
	msix := filepath.Join(w.root, "AppData", "Local", "Packages", "NousResearch.Hermes_8wekyb3d8bbwe")
	mkdir(t, msix)
	w.plat.Paths.(*platform.FakePaths).Packaged = []platform.PackagedHint{
		{Kind: "msix", Path: msix, Use: "the Microsoft Store version updates itself through the Store"},
	}
	before := w.storeBytes()
	w.cli("--plain")
	w.transcript("packaged-msix")
	w.wantCode(apperr.NotCheckout)
	w.wantScreen("This Hermes install is a msix, not a managed source checkout", "updates itself through the Store")
	if c := w.Calls(); len(c) != 0 {
		t.Errorf("ran %v", c)
	}
	w.nothingChanged(before)
}

func TestE2EUntestedPlatformGate(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		keys []conStep
		code int
	}{
		{"unattended-refused", []string{"--plain", "--unattended"}, nil, apperr.Cancelled},
		{"declined", []string{"--plain"}, []conStep{{key: 'n'}}, apperr.Cancelled},
		{"accepted-check", []string{"--plain", "--accept-untested", "check"}, nil, apperr.OK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.plat.OS, w.plat.Tested = "linux", false
			w.con.script(tc.keys...)
			before := w.storeBytes()
			w.cli(tc.args...)
			w.wantCode(tc.code)
			if tc.code != apperr.OK {
				w.wantScreen("only been tested on Windows")
				if c := w.Calls(); len(c) != 0 {
					t.Errorf("ran %v before the gate", c)
				}
			} else {
				w.wantScreen("Update available. Nothing was changed.")
				w.noScreen("only been tested on Windows")
			}
			w.nothingChanged(before)
		})
	}
}

// TestReplayLive is the dev replay for the demo video (task D2): it plays a
// scripted run into this console in (scaled) real time, VT renderer, with
// the same fakes as the scenarios. Skipped unless asked for:
//
//	HSU_REPLAY=demo HSU_REPLAY_SPEED=8 go test ./cmd/hermes-safe-update -run TestReplayLive -v -count=1
//
// HSU_REPLAY: demo (check, then busy -> idle -> update with settings
// migration) | happy | check. HSU_REPLAY_SPEED: virtual seconds per real
// second (default 10). Nothing outside a temp dir is touched.
func TestReplayLive(t *testing.T) {
	which := os.Getenv("HSU_REPLAY")
	if which == "" {
		t.Skip("set HSU_REPLAY=demo|happy|check to play a replay")
	}
	speed := 10.0
	if s := os.Getenv("HSU_REPLAY_SPEED"); s != "" {
		if _, err := fmt.Sscan(s, &speed); err != nil || speed <= 0 {
			t.Fatalf("HSU_REPLAY_SPEED=%q", s)
		}
	}
	play := func(args []string, setup func(w *e2eWorld)) {
		w := newWorld(t)
		w.vt, w.pace, w.live = true, speed, os.Stdout
		setup(w)
		w.cli(args...)
		fmt.Fprintf(os.Stdout, "\nFinished with exit code %d.\n", w.code)
	}
	if which == "check" || which == "demo" {
		play([]string{"check"}, func(w *e2eWorld) {})
	}
	switch which {
	case "demo":
		play(nil, func(w *e2eWorld) {
			w.busySession()
			w.con.script(conStep{timeout: true, then: (*e2eWorld).endSession}, conStep{key: 'y'})
		})
	case "happy":
		play(nil, func(w *e2eWorld) { w.con.script(conStep{key: 'y'}) })
	case "check":
	default:
		t.Fatalf("HSU_REPLAY=%q (want demo, happy or check)", which)
	}
}

func TestE2ECheckChangesNothing(t *testing.T) {
	modes(t, func(t *testing.T, vt bool) {
		w := newWorld(t)
		w.vt = vt
		w.busySession()
		before := w.storeBytes()
		w.cli("check")
		w.transcript("check")
		w.wantCode(apperr.OK)
		w.wantScreen("Update available. Nothing was changed.", "3 new commit(s)", "Planning the week",
			"2 old dependency set(s), ~612 MB to free")
		w.nothingChanged(before)
		if w.con.prompts != 0 {
			t.Errorf("check asked %d question(s)", w.con.prompts)
		}
		if bs, _ := filepath.Glob(filepath.Join(w.home, "backups", "*")); len(bs) != 0 {
			t.Errorf("check wrote backups: %v", bs)
		}
	})
}

// Brief: the settings backup manifest records "Hermes version before/after".
// A stand-alone backup-settings / migrate-settings has no update: both are
// the current `hermes --version`.
func TestE2EStandaloneSettingsBackupRecordsCurrentVersion(t *testing.T) {
	for _, cmd := range []string{"backup-settings", "migrate-settings"} {
		t.Run(cmd, func(t *testing.T) {
			w := newWorld(t)
			w.procs.Remove(pidDesktop) // settings commands need Hermes closed
			w.cli("--plain", cmd)
			w.wantCode(apperr.OK)
			bs, _ := filepath.Glob(filepath.Join(w.home, "backups", "settings-*", "manifest.json"))
			if len(bs) != 1 {
				t.Fatalf("backups %v\n%s", bs, w.screen())
			}
			raw, err := os.ReadFile(bs[0])
			if err != nil {
				t.Fatal(err)
			}
			var m struct {
				Before string `json:"hermes_before"`
				After  string `json:"hermes_after"`
			}
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			if m.Before != "v0.21.5" || m.After != "v0.21.5" {
				t.Errorf("manifest versions = %q -> %q, want v0.21.5 -> v0.21.5", m.Before, m.After)
			}
		})
	}
}

// In a full run: before = pre-flight version, after = the version the
// updated launcher reports.
func TestE2EUpdateManifestRecordsBeforeAndAfterVersion(t *testing.T) {
	w := newWorld(t)
	w.con.script(conStep{key: 'y'})
	w.cli("--plain")
	w.wantCode(apperr.OK)
	bs, _ := filepath.Glob(filepath.Join(w.home, "backups", "settings-*", "manifest.json"))
	if len(bs) != 1 {
		t.Fatalf("backups %v", bs)
	}
	raw, _ := os.ReadFile(bs[0])
	var m struct {
		Before string `json:"hermes_before"`
		After  string `json:"hermes_after"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.Before != "v0.21.5" || m.After != "v0.21.6" {
		t.Errorf("manifest versions = %q -> %q, want v0.21.5 -> v0.21.6", m.Before, m.After)
	}
}

// RT-4F: upstream removed renderer-server.ts and loads the renderer from
// file:// again. The origin is detected from main.ts; the stage migrates
// the old 127.0.0.1 store into file://.
func TestE2ESettingsTargetIsFileWhenRendererServerIsGone(t *testing.T) {
	w := newWorld(t)
	rs := filepath.Join(w.checkout, filepath.FromSlash(hermes.RendererServerSource))
	if err := os.Remove(rs); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(w.checkout, filepath.FromSlash(hermes.DesktopMainSource)),
		"const u = `${pathToFileURL(resolveRendererIndex()).toString()}?win=quick#/`\n")
	all := w.store()
	all["file://"], all[targetOrigin] = all[targetOrigin], all["file://"]
	w.writeStore(all)
	w.con.script(conStep{key: 'y'})
	w.cli("--plain")
	w.wantCode(apperr.OK)
	w.wantScreen("migrated " + targetOrigin + " -> file://")
}

// RT-4F: when the origin cannot be detected the stage still takes a backup
// and the WARN names it and the exact command.
func TestE2ESettingsOriginUnknownStillBacksUp(t *testing.T) {
	w := newWorld(t)
	rs := filepath.Join(w.checkout, filepath.FromSlash(hermes.RendererServerSource))
	if err := os.Remove(rs); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(w.checkout, filepath.FromSlash(hermes.DesktopMainSource)), "const something = 1\n")
	before := w.storeBytes()
	w.con.script(conStep{key: 'y'})
	w.cli("--plain")
	w.wantCode(apperr.OK)
	w.wantScreen("WARN desktop settings migration failed", "cannot detect the target origin",
		"settings backup settings-", "hermes-safe-update migrate-settings --origin <url>", "Hermes is updated and verified.", "Warnings")
	if !bytes.Equal(w.storeBytes(), before) {
		t.Error("live store changed")
	}
	bs, _ := filepath.Glob(filepath.Join(w.home, "backups", "settings-*", "manifest.json"))
	if len(bs) != 1 {
		t.Errorf("want exactly one backup, got %v", bs)
	}
}
