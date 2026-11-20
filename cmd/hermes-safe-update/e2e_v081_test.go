package main

import (
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
)

// v0.8.1 end to end: Hermes' own hold at apply time, and the plugin catalog
// notice at the end of a v0.23.0 update. The real CLI against the synthetic
// install, Hermes' answers scripted.

// holdApplyUpdate is `hermes update` on a release channel that holds the
// build (synthetic text of Hermes v0.22.7 / v0.23.0): HEAD and the gateway
// stay where they are.
func holdApplyUpdate() []updateLine {
	return []updateLine{
		{0, "☤ Updating Hermes Agent...", nil},
		{0, "→ Update channel: stable", nil},
		{time.Second, "→ Latest release: stable v0.22.1 (abcdef012345)", nil},
		{3 * time.Second, "→ Fetching updates...", nil},
		{time.Second, "→ This build is newer than the stable release abcdef012345: that release predates the gateway runtime your sessions use, so there is nothing to update to yet.", nil},
		{0, "  Hermes stays on 0123456789ab until a newer stable release ships. To follow every commit instead: `hermes update --set-channel main`.", nil},
		{2 * time.Second, "  → Updating dependencies…", nil},
		{3 * time.Second, "✓ Holding at this build until a newer stable release ships.", nil},
	}
}

func TestE2EHermesHoldAtApplyIsNotAnUpdate(t *testing.T) {
	const headline = "Hermes did not update: it held this build (the stable release abcdef012345 predates the gateway runtime your sessions use)."
	for _, mode := range []string{"attended", "unattended"} {
		w := stableWorld(t, "update-check-stable-available.txt")
		w.relCompare = "ahead"
		w.update = holdApplyUpdate()
		args := []string{"--plain"}
		if mode == "attended" {
			w.con.script(conStep{key: 'y'})
		} else {
			args = append(args, "--unattended")
		}
		w.cli(args...)
		w.transcript("hermes-hold-apply-" + mode)
		w.wantCode(apperr.OK)
		w.wantScreen(headline, "Hermes held", "hermes-safe-update channel set main", "RESULT: OK")
		w.noScreen("Hermes is updated and verified", "Version ", "PROBLEM")
		if w.Head() != e2eOld {
			t.Errorf("%s: head moved to %s", mode, w.Head())
		}
		if !w.ran(w.launcher, "update", "--yes", "--keep-stash") {
			t.Errorf("%s: hermes update did not run", mode)
		}
	}
}

func TestE2ECatalogNoticeIsFoldedIntoThePluginsRow(t *testing.T) {
	w := stableWorld(t, "update-check-stable-available.txt")
	w.relCompare = "ahead"
	w.vt = true
	w.extrasWorld()
	w.record = "stable" // an explicit record keeps config.yaml (the plugin settings live in it)
	w.update = append(stableUpdate(),
		updateLine{0, "  → 2 plugin update(s) from the new catalog in this profile: memory-demo, notes-demo", nil},
		updateLine{0, "    apply: hermes plugins update   (or turn on: hermes plugins auto-update on)", nil})
	// the update, the plugin question, the skills question
	w.con.script(conStep{key: 'y'}, conStep{key: 'y'}, conStep{key: 'y'})
	w.cli()
	w.transcript("catalog-notice-folded")
	w.wantCode(apperr.OK)
	w.wantScreen("Hermes is updated and verified.", "Update memory-demo (your memory provider)", "1 of 1 updated: memory-demo",
		"Hermes's notice after the update also lists notes-demo, which the check did not offer")
	w.noScreen("plugin update(s) from the new catalog", "apply: hermes plugins update   (or turn on")
	if n := strings.Count(w.screen(), "Update memory-demo"); n != 1 {
		t.Errorf("the plugin question appeared %d times", n)
	}
}
