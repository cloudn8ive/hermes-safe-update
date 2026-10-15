package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
)

// End-to-end scenarios of the release channels (Safe Update v0.3.0): the
// real CLI against the synthetic install, Hermes' answers scripted. The
// default world keeps a channel record "main" in the Hermes config, so the
// older scenarios (follow resolves to main) run the main path unchanged.

func readContract(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "internal", "hermes", "testdata", "contracts", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// stableWorld is a checkout with no channel record on the official origin:
// Hermes' own channel is stable.
func stableWorld(t *testing.T, check string) *e2eWorld {
	t.Helper()
	w := newWorld(t)
	w.record = "none"
	w.releaseOut = readContract(t, check)
	w.applyArgv = []string{"update", "--yes", "--keep-stash"} // follow: no channel flag
	w.update = stableUpdate()
	return w
}

// stableUpdate is the output of a release `hermes update`: no "Found N new
// commit(s)", a "Switching to source commit" instead.
func stableUpdate() []updateLine {
	return []updateLine{
		{0, "→ Update channel: stable", nil},
		{time.Second, "→ Latest release: stable v0.22.1 (abcdef012345)", nil},
		{2 * time.Second, "→ Stopping Windows gateway (pid 8211) so files can be replaced…", (*e2eWorld).removeGateways},
		{3 * time.Second, "→ Fetching updates...", nil},
		{9 * time.Second, "→ Switching to source commit abcdef0123", func(w *e2eWorld) { w.setHead(e2eNew) }},
		{time.Second, "→ Pulling updates...", nil},
		{40 * time.Second, "→ Preparing Node dependencies… (package.json changed: npm ci)", nil},
		{60 * time.Second, "→ Building desktop packaged app…", nil},
		{20 * time.Second, "✓ Code updated!", nil},
		{10 * time.Second, "→ Restarting the gateway…", (*e2eWorld).startGateway},
		{20 * time.Second, "✓ Update complete! (v0.22.0 → v0.22.1) [detached @ abcdef0]", nil},
	}
}

// noMainTraffic asserts that a release run never touched what only answers
// questions about main: the GitHub compare/commit API for main, origin/main
// in git, `git ls-remote`, `--branch main`. (A release run does ask the API
// about the release: the compare against its tag and releases/latest.)
func (w *e2eWorld) noMainTraffic() {
	w.t.Helper()
	w.mu.Lock()
	paths := append([]string(nil), w.apiPaths...)
	w.mu.Unlock()
	for _, p := range paths {
		if strings.HasSuffix(p, "...main") || strings.Contains(p, "/commits/main") {
			w.t.Errorf("the GitHub API was asked about main on a release channel: %s", p)
		}
	}
	for _, c := range w.Calls() {
		if j := strings.Join(c, " "); strings.Contains(j, "origin/main") || strings.Contains(j, "ls-remote") || strings.Contains(j, "--branch main") {
			w.t.Errorf("a main-only command ran on a release channel: %s", j)
		}
	}
}

func TestE2EStableFlow(t *testing.T) {
	modes(t, func(t *testing.T, vt bool) {
		w := stableWorld(t, "update-check-stable-available.txt")
		w.vt = vt
		w.relCompare = "ahead" // the release is not in the checkout yet: GitHub compares HEAD with its tag
		w.con.script(conStep{key: 'y'})
		w.cli()
		w.wantCode(apperr.OK)
		w.wantScreen("Using the stable channel (Hermes' default for this checkout)", "release v0.22.1 available, 7 commits (Hermes release check)",
			"Hermes is updated and verified.", "v0.22.0 → v0.22.1  (3 commits)", "stable (Hermes default)", "Hermes release check (channel stable)",
			"hermes update --yes --keep-stash")
		w.noScreen("on main", "more commits landed", "--branch main", "--channel", "was published while this ran")
		w.noMainTraffic()
		if got := w.Head(); got != e2eNew {
			t.Errorf("head %s", got)
		}
		if !w.ran(w.launcher, "update", "--check") {
			t.Error("Hermes' own check never ran")
		}
	})
}

// The golden of a whole stable run: the rows of the release path in order.
func TestE2EHappyPathStable(t *testing.T) {
	w := stableWorld(t, "update-check-stable-available.txt")
	w.relCompare = "ahead"
	w.latest = `{"tag_name":"v0.22.1","draft":false,"prerelease":false}`
	w.latestCmp = "identical"
	w.con.script(conStep{key: 'y'})
	w.cli("--plain")
	w.transcript("happy-path-stable")
	w.wantCode(apperr.OK)
	w.wantScreen("release v0.22.1 available, 7 commits", "node packages", "changed, will be reinstalled", "stable (Hermes default)")
	w.noScreen("Newer")
	w.noMainTraffic()
}

// The checkout already has the release (an earlier run or the local mirror
// put it there): git answers, GitHub is not asked to compare.
func TestE2EStableReleaseInTheCheckoutIsPredictedByGit(t *testing.T) {
	w := stableWorld(t, "update-check-stable-available.txt")
	w.releaseLocal = true
	w.cli("--plain", "check")
	w.wantCode(apperr.OK)
	w.wantScreen("release v0.22.1 available, 7 commits", "Node packages", "changed, will be reinstalled", "release v0.22.1 (7 commits)")
	w.noMainTraffic()
	for _, p := range w.apiPaths {
		if strings.Contains(p, "/compare/") {
			t.Errorf("GitHub was asked to compare although git had the release: %s", p)
		}
	}
	if !w.ran(w.git, "-c", "gc.auto=0", "--no-optional-locks", "-C", w.checkout, "diff", "--quiet", "HEAD", "refs/tags/v0.22.1") {
		t.Error("the prediction did not diff against the release tag")
	}
}

func TestE2EStableNewerReleasePublishedDuringTheRun(t *testing.T) {
	t.Run("a newer release", func(t *testing.T) {
		w := stableWorld(t, "update-check-stable-available.txt")
		w.latest = `{"tag_name":"v0.22.2","draft":false,"prerelease":false}`
		w.latestCmp, w.latestN = "ahead", 4
		w.con.script(conStep{key: 'y'})
		w.cli("--plain")
		w.wantCode(apperr.OK)
		w.wantScreen("release v0.22.2 was published while this ran; run Safe Update again when convenient")
		w.noMainTraffic()
	})
	t.Run("main moved but no release did", func(t *testing.T) {
		w := stableWorld(t, "update-check-stable-available.txt")
		w.latest = `{"tag_name":"v0.22.1","draft":false,"prerelease":false}`
		w.latestCmp = "identical"
		w.newerMode, w.newerN = "ahead", 90 // main is far ahead: not news on a release channel
		w.lsRemoteTip = strings.Repeat("e", 40)
		w.con.script(conStep{key: 'y'})
		w.cli("--plain")
		w.wantCode(apperr.OK)
		w.noScreen("Newer", "landed on GitHub", "was published while this ran")
		w.noMainTraffic()
	})
	t.Run("the API is down", func(t *testing.T) {
		w := stableWorld(t, "update-check-stable-available.txt")
		w.con.script(conStep{key: 'y'})
		w.cli("--plain")
		w.wantCode(apperr.OK)
		w.wantScreen("Hermes is updated and verified.")
		w.noScreen("Newer")
	})
}

func TestE2EStableChannelFromYamlPassesChannelFlag(t *testing.T) {
	w := newWorld(t)
	w.yaml += "update:\n  channel: \"stable\"\n"
	w.releaseOut = readContract(t, "update-check-stable-available.txt")
	w.applyArgv = []string{"update", "--yes", "--keep-stash", "--channel", "stable"}
	w.update = stableUpdate()
	w.con.script(conStep{key: 'y'})
	w.cli("--plain")
	w.wantCode(apperr.OK)
	w.wantScreen("hermes update --yes --keep-stash --channel stable", "Using the stable channel (update.channel in safe-update.yaml)")
	if !w.ran(w.launcher, "update", "--check", "--channel", "stable") {
		t.Error("the check did not carry --channel stable")
	}
	w.noMainTraffic()
}

func TestE2ECheckStableAvailable(t *testing.T) {
	w := stableWorld(t, "update-check-stable-available.txt")
	w.busySession()
	before := w.storeBytes()
	w.cli("--plain", "check")
	w.transcript("check-stable-available")
	w.wantCode(apperr.OK)
	w.wantScreen("Update available. Hermes was not touched.", "release stable v0.22.1", "Hermes release check (channel stable)", "Planning the week")
	w.noScreen("on main")
	w.nothingChanged(before)
	w.noMainTraffic()
	if w.con.prompts != 0 {
		t.Errorf("check asked %d question(s)", w.con.prompts)
	}
}

func TestE2ENoUpdateStableWaitsForTheNextRelease(t *testing.T) {
	w := stableWorld(t, "update-check-stable-waits.txt")
	before := w.storeBytes()
	w.cli("--plain")
	w.transcript("no-update-stable-waits")
	w.wantCode(apperr.OK)
	w.wantScreen("none, checkout is newer than release stable v0.22.0; waits for the next release", "Nothing to update", "Hermes is up to date")
	w.nothingChanged(before)
	w.noMainTraffic()
}

func TestE2EChannelUnresolvedStopsBeforeAnythingCloses(t *testing.T) {
	w := stableWorld(t, "update-check-stable-unresolved.txt")
	before := w.storeBytes()
	w.cli("--plain")
	w.transcript("channel-unresolved")
	w.wantCode(apperr.PreflightStop)
	w.wantScreen("Hermes could not resolve the stable release", "Connection to api.github.com failed", "Nothing was changed")
	w.noScreen("Hermes is up to date")
	w.nothingChanged(before)
}

// The check of an explicit stable record: the checkout is newer than the
// release, so `hermes update` would move it back.
const rollbackCheck = "→ Update channel: stable\n" +
	"→ This checkout is newer than the selected release: stable v0.21.4 (0123456789ab).\n" +
	"  `hermes update` moves it back to that release (`hermes update --set-channel main` follows every commit).\n"

func rollbackWorld(t *testing.T) *e2eWorld {
	w := newWorld(t)
	w.record = "stable"
	w.releaseOut = rollbackCheck
	w.applyArgv = []string{"update", "--yes", "--keep-stash"}
	w.update = stableUpdate()
	return w
}

func TestE2ERollbackRefusedUnattended(t *testing.T) {
	w := rollbackWorld(t)
	before := w.storeBytes()
	w.cli("--plain", "--unattended")
	w.transcript("rollback-refused-unattended")
	w.wantCode(apperr.PreflightStop)
	w.wantScreen("move Hermes BACK from v0.21.5 to stable v0.21.4", "never rolls back", "hermes-safe-update run", "hermes update")
	w.nothingChanged(before)
	if w.con.prompts != 0 {
		t.Errorf("an unattended run asked %d question(s)", w.con.prompts)
	}
}

func TestE2ERollbackAttended(t *testing.T) {
	t.Run("refused", func(t *testing.T) {
		w := rollbackWorld(t)
		w.con.script(conStep{key: 'n'})
		before := w.storeBytes()
		w.cli("--plain")
		w.wantCode(apperr.PreflightStop)
		w.wantScreen("BACK from v0.21.5 to stable v0.21.4", "not rolled back")
		w.nothingChanged(before)
	})
	t.Run("timeout", func(t *testing.T) {
		w := rollbackWorld(t)
		w.con.script(conStep{timeout: true})
		before := w.storeBytes()
		w.cli("--plain")
		w.wantCode(apperr.PreflightStop)
		w.nothingChanged(before)
	})
	t.Run("confirmed", func(t *testing.T) {
		w := rollbackWorld(t)
		w.con.script(conStep{key: 'y'}, conStep{key: 'y'}) // the rollback, then the usual confirmation
		w.cli("--plain")
		w.wantCode(apperr.OK)
		w.wantScreen("BACK from v0.21.5 to stable v0.21.4", "rollback confirmed", "Hermes is updated and verified.")
		if w.Head() != e2eNew {
			t.Errorf("head %s", w.Head())
		}
	})
}

func TestE2EStableOnAnOldHermesStopsPreflight(t *testing.T) {
	w := newWorld(t)
	w.help = "legacy"
	before := w.storeBytes()
	w.cli("--plain", "--channel", "stable")
	w.wantCode(apperr.PreflightStop)
	w.wantScreen("needs Hermes v0.21.6 or newer", "update.channel: main")
	w.nothingChanged(before)
}

func TestE2EFollowOnAnOldHermesKeepsTheMainArgv(t *testing.T) {
	w := newWorld(t)
	w.help = "legacy"
	w.record = "none"
	w.con.script(conStep{key: 'y'})
	w.cli("--plain")
	w.wantCode(apperr.OK)
	w.wantScreen("3 new commit(s) on main (GitHub API)", "hermes update --yes --keep-stash --branch main")
}

func TestE2EChannelFlagBeatsTheYaml(t *testing.T) {
	t.Run("flag stable over yaml main", func(t *testing.T) {
		w := newWorld(t)
		w.yaml += "update:\n  channel: \"main\"\n"
		w.releaseOut = readContract(t, "update-check-stable-available.txt")
		w.cli("--plain", "check", "--channel", "stable")
		w.wantCode(apperr.OK)
		w.wantScreen("release stable v0.22.1")
		w.noMainTraffic()
	})
	t.Run("flag main over yaml stable", func(t *testing.T) {
		w := newWorld(t)
		w.yaml += "update:\n  channel: \"stable\"\n"
		w.cli("--plain", "--channel", "main", "check")
		w.wantCode(apperr.OK)
		w.wantScreen("3 new commit(s) on main (GitHub API)")
		if w.ran(w.launcher, "update", "--check", "--channel", "stable") {
			t.Error("the yaml's stable ran despite --channel main")
		}
	})
}

func TestE2EBadChannelIsAConfigError(t *testing.T) {
	w := newWorld(t)
	w.cli("--plain", "--channel", "beta")
	w.wantCode(apperr.Usage)
	if !strings.Contains(w.errb.String(), "update.channel") {
		t.Errorf("stderr: %s", w.errb.String())
	}
	w = newWorld(t)
	w.yaml += "update:\n  channel: \"beta\"\n"
	w.cli("--plain", "check")
	w.wantCode(apperr.Usage)
	if !strings.Contains(w.errb.String(), "safe-update.yaml:") || !strings.Contains(w.errb.String(), "update.channel: must be one of follow, stable, main") {
		t.Errorf("stderr: %s", w.errb.String())
	}
	w = newWorld(t)
	w.cli("--plain", "--channel", "stable", "list-backups")
	w.wantCode(apperr.Usage)
}
