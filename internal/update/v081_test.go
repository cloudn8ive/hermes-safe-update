package update

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
	"github.com/cloudn8ive/hermes-safe-update/internal/ui"
)

// v0.8.1: Hermes' apply-time hold, the end-of-update catalog notice, and
// plugins pinned with --ref.

// ---- 1. the apply-time hold ----

func holdUpdateHarness(t *testing.T, out string, advance bool) (*harness, *[]ui.Card, **Run) {
	t.Helper()
	h := newHarness(t)
	h.onUpdate(0, out, advance)
	cards := &[]ui.Card{}
	h.cards = cards
	run := new(*Run)
	h.onRun = func(r *Run) { *run = r }
	return h, cards, run
}

func hooksOn(h *harness) {
	h.cfg.Hooks = []config.Hook{
		{Name: "post-update-hook", When: config.HookPostUpdate, Run: []string{"after-update.exe"}},
		{Name: "post-relaunch-hook", When: config.HookPostRelaunch, Run: []string{"after-relaunch.exe"}},
	}
	h.runner.On([]string{"after-update.exe"}, execx.Result{Output: "merged"})
	h.runner.On([]string{"after-relaunch.exe"}, execx.Result{Output: "notified"})
}

func ranHook(h *harness, exe string) bool {
	for _, c := range h.runner.Calls() {
		if c.Argv[0] == exe {
			return true
		}
	}
	return false
}

func checkHeldCard(t *testing.T, h *harness, c ui.Card) {
	t.Helper()
	const wantHead = "Hermes did not update: it held this build (the stable release 0123456789ab predates the gateway runtime your sessions use)."
	if c.Headline != wantHead {
		t.Errorf("headline = %q\nwant       %q", c.Headline, wantHead)
	}
	if strings.Contains(c.Headline, "updated and verified") {
		t.Error("the card claims an update")
	}
	if _, has := rowOf(c, "Version"); has {
		t.Error("a Version row on a run that did not update")
	}
	r := wantRow(t, c, "Hermes held", "Hermes kept this build", "until a newer stable release ships", "hermes-safe-update channel set main")
	if r.Verdict != nil {
		t.Errorf("held row verdict = %v", *r.Verdict)
	}
	if c.OK == nil || !*c.OK {
		t.Errorf("card OK = %v: a hold is not a failure", c.OK)
	}
}

func TestHermesHoldAtApplyAttended(t *testing.T) {
	h, cards, run := holdUpdateHarness(t, fixture(t, "update-apply-hold.txt"), false)
	hooksOn(h)
	h.con.Keys = []platform.Key{key('y')}
	err := h.run()
	if err != nil {
		t.Fatalf("a hold is exit 0, got %v\n%s", err, h.output())
	}
	if apperr.ExitCode(err) != 0 {
		t.Errorf("exit = %d", apperr.ExitCode(err))
	}
	checkHeldCard(t, h, lastCard(t, cards))
	f := (*run).Facts
	if f.HermesHeld == nil || f.HermesHeld.Channel != "stable" || !f.Verified || f.Again != AgainNone {
		t.Errorf("facts: held=%+v verified=%v again=%q", f.HermesHeld, f.Verified, f.Again)
	}
	if strings.Contains(h.output(), "Hermes is updated and verified") {
		t.Errorf("the screen says updated:\n%s", h.output())
	}
	// Hermes still comes back: it was closed for the update.
	if calls := h.app.Calls(); len(calls) < 2 || calls[0] != "close 100" || calls[len(calls)-1] != "launch "+tDesktop {
		t.Errorf("app calls = %v", calls)
	}
	// verify still ran (Hermes did its up-to-date tail), the cleanup too; the hooks did not
	if len(h.gc.runs) != 1 {
		t.Errorf("cleanup ran %d times", len(h.gc.runs))
	}
	if ranHook(h, "after-update.exe") {
		t.Error("a post-update hook ran after a hold")
	}
	h.mustContain("RESULT: OK")
}

func TestHermesHoldAtApplyUnattended(t *testing.T) {
	h, cards, run := holdUpdateHarness(t, fixture(t, "update-apply-hold.txt"), false)
	hooksOn(h)
	if err := h.run("unattended"); err != nil {
		t.Fatalf("a hold is exit 0, got %v\n%s", err, h.output())
	}
	checkHeldCard(t, h, lastCard(t, cards))
	if (*run).Facts.HermesHeld == nil {
		t.Error("not recorded")
	}
	if ranHook(h, "after-update.exe") {
		t.Error("a post-update hook ran after a hold")
	}
	// the app was running, so an unattended run gives it back
	if calls := h.app.Calls(); len(calls) < 2 || calls[len(calls)-1] != "launch "+tDesktop {
		t.Errorf("app calls = %v", calls)
	}
}

func TestHermesHoldOnBetaNamesBeta(t *testing.T) {
	h, cards, _ := holdUpdateHarness(t, fixture(t, "update-apply-hold-beta.txt"), false)
	if err := h.run("unattended"); err != nil {
		t.Fatalf("err = %v", err)
	}
	c := lastCard(t, cards)
	if !strings.Contains(c.Headline, "the beta release fedcba987654 predates") || strings.Contains(c.Headline, "stable") {
		t.Errorf("headline = %q", c.Headline)
	}
	wantRow(t, c, "Hermes held", "until a newer beta release ships")
}

// A hold with a moved HEAD is not a hold: HEAD is trusted, with a WARN.
func TestHermesHoldButHeadMovedTrustsHead(t *testing.T) {
	h, cards, run := holdUpdateHarness(t, fixture(t, "update-apply-hold.txt"), true)
	var log strings.Builder
	h.logSink = &log
	if err := h.run("unattended"); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	c := lastCard(t, cards)
	if (*run).Facts.HermesHeld != nil || strings.Contains(c.Headline, "did not update") {
		t.Errorf("held was trusted over HEAD: %+v / %q", (*run).Facts.HermesHeld, c.Headline)
	}
	if !strings.Contains(c.Headline, "updated and verified") {
		t.Errorf("headline = %q", c.Headline)
	}
	if _, has := rowOf(c, "Hermes held"); has {
		t.Error("held row on a run that moved")
	}
	if !strings.Contains(log.String(), "said it held this build, but HEAD moved") || (*run).Facts.Warnings != 1 {
		t.Errorf("no WARN (warnings=%d):\n%s", (*run).Facts.Warnings, log.String())
	}
}

func TestHermesHoldNotTakenWhenTheUpdateFailsOrCompletes(t *testing.T) {
	hold := fixture(t, "update-apply-hold.txt")
	for _, c := range []struct {
		name string
		rc   int
		out  string
		want string
	}{
		{"exit 1", 1, hold, "needs attention"},
		{"hold and Update complete!", 0, hold + "\n✓ Update complete! (v1.0 → v1.1)\n", "updated and verified"},
	} {
		h, cards, run := holdUpdateHarness(t, c.out, false)
		h.onUpdate(c.rc, c.out, false)
		err := h.run("unattended")
		if c.rc != 0 && !errors.Is(err, apperr.ErrUpdateProblem) {
			t.Errorf("%s: err = %v", c.name, err)
		}
		if (*run).Facts.HermesHeld != nil {
			t.Errorf("%s: taken as a hold", c.name)
		}
		if got := lastCard(t, cards).Headline; strings.Contains(got, "did not update") || !strings.Contains(got, c.want) {
			t.Errorf("%s: headline %q", c.name, got)
		}
	}
}

func TestHermesHoldNeedsHeadToBeReadable(t *testing.T) {
	h, cards, run := holdUpdateHarness(t, fixture(t, "update-apply-hold.txt"), false)
	var log strings.Builder
	h.logSink = &log
	// pre-flight reads HEAD fine; the read after the update fails
	out := fixture(t, "update-apply-hold.txt")
	h.update = func(int) execx.Result {
		h.repo.mu.Lock()
		h.repo.headErr = true
		h.repo.mu.Unlock()
		return execx.Result{Output: out}
	}
	if err := h.run("unattended"); err != nil {
		t.Logf("err = %v", err)
	}
	if (*run).Facts.HermesHeld != nil {
		t.Error("held without being able to read HEAD")
	}
	if !strings.Contains(log.String(), "HEAD after the update could not be read") {
		t.Errorf("the reason is not logged:\n%s", log.String())
	}
	if got := lastCard(t, cards).Headline; strings.Contains(got, "did not update") {
		t.Errorf("headline %q", got)
	}
}

// The repair path: Hermes' hold text replaces "Already up to date!", and its
// completion steps ran: the repair is finished, with a row naming the hold.
func TestHermesHoldDuringRepair(t *testing.T) {
	h := offerHarness(t)
	h.onUpdate(0, fixture(t, "update-apply-hold.txt"), false)
	cards := &[]ui.Card{}
	h.cards = cards
	h.con.Keys = []platform.Key{key('r')}
	if err := h.run(); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	c := lastCard(t, cards)
	if !strings.Contains(c.Headline, "Repair finished and verified.") {
		t.Errorf("headline = %q", c.Headline)
	}
	wantRow(t, c, "Hermes held", "Hermes kept this build", "The steps of the repair ran on this build")
}

func TestUpdateChildGetsUTF8(t *testing.T) {
	h, _, _ := holdUpdateHarness(t, fixture(t, "update-apply-hold.txt"), false)
	if err := h.run("unattended"); err != nil {
		t.Fatal(err)
	}
	if got := h.updateCmd.Env["PYTHONIOENCODING"]; got != "utf-8" {
		t.Errorf("PYTHONIOENCODING = %q: the → of Hermes' lines would not survive", got)
	}
}

// ---- 2. the catalog notice ----

const noticeThisProfile = "  → 1 plugin update(s) from the new catalog in this profile: memory-demo\n    apply: hermes plugins update   (or turn on: hermes plugins auto-update on)\n"

func noticeHarness(t *testing.T, notice string) (*harness, *xs, *[]ui.Card) {
	t.Helper()
	h, s, cards := extrasHarness(t)
	h.mainCheck = "3 commits behind origin/main"
	h.onUpdate(0, "→ Fetching updates\nInstalling Python dependencies…\n✓ Update complete! (v1.0 → v1.1)\n"+notice, true)
	return h, s, cards
}

func TestCatalogNoticeAgreesWithTheProbeAndIsAskedOnce(t *testing.T) {
	h, _, cards := noticeHarness(t, noticeThisProfile)
	var log strings.Builder
	h.logSink = &log
	h.con.Keys = []platform.Key{key('y'), key('y'), key('y')} // the update, the plugin, the skills
	if err := h.run(); err != nil {
		t.Fatalf("run: %v\n%s", err, h.output())
	}
	if n := h.xRan("plugins", "update", "memory-demo"); n != 1 {
		t.Errorf("plugin updated %d times", n)
	}
	if n := strings.Count(h.output(), "Update memory-demo "); n != 1 {
		t.Errorf("the plugin question was asked %d times:\n%s", n, h.output())
	}
	c := lastCard(t, cards)
	plugins := 0
	for _, r := range c.Rows {
		if strings.HasPrefix(r.Key, "Plugin") {
			plugins++
		}
	}
	if plugins != 1 {
		t.Errorf("%d plugin rows: %+v", plugins, c.Rows)
	}
	r := wantRow(t, c, "Plugins", "1 of 1 updated: memory-demo")
	if strings.Contains(r.Value, "notice") {
		t.Errorf("the notice adds nothing when the names agree: %q", r.Value)
	}
	// no separate line on the screen, both lines in the log
	if strings.Contains(h.output(), "plugin update(s) from the new catalog") || strings.Contains(h.output(), "apply: hermes plugins update") {
		t.Errorf("Hermes' notice reached the screen:\n%s", h.output())
	}
	if !strings.Contains(log.String(), "plugin catalog notice") || !strings.Contains(log.String(), "  | "+strings.TrimRight(strings.Split(noticeThisProfile, "\n")[0], "\n")) {
		t.Errorf("notice not logged:\n%s", log.String())
	}
}

func TestCatalogNoticeNamesWhatTheProbeDidNotOffer(t *testing.T) {
	notice := "  → 2 plugin update(s) from the new catalog in this profile: memory-demo, notes-demo\n    apply: hermes plugins update   (or turn on: hermes plugins auto-update on)\n" +
		"  → 1 plugin update(s) from the new catalog in work: other-demo\n    apply: hermes plugins update   (or turn on: hermes plugins auto-update on)\n"
	h, _, cards := noticeHarness(t, notice)
	h.con.Keys = []platform.Key{key('y'), key('y'), key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("run: %v\n%s", err, h.output())
	}
	if h.xRan("plugins", "update", "notes-demo") != 0 || h.xRan("plugins", "update", "other-demo") != 0 {
		t.Errorf("a plugin only the notice named was updated: %v", h.calls())
	}
	if strings.Contains(h.output(), "Update notes-demo") {
		t.Error("asked about a plugin only the notice named")
	}
	c := lastCard(t, cards)
	wantRow(t, c, "Plugins", "1 of 1 updated: memory-demo", "Hermes's notice after the update also lists notes-demo, which the check did not offer",
		"apply: hermes plugins update <name>", "Hermes also lists 1 update in work: other-demo")
}

func TestCatalogNoticeWhenTheProbeFoundNothing(t *testing.T) {
	h, s, cards := noticeHarness(t, noticeThisProfile)
	s.plugins = xPluginsCurrent
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	r := wantRow(t, lastCard(t, cards), "Plugins", "Hermes's notice after the update also lists memory-demo", "which the check did not offer")
	if strings.Contains(r.Value, "up to date") || r.Verdict == nil || *r.Verdict != ui.VerdictAttention {
		t.Errorf("row %+v says up to date next to a notice", r)
	}
}

func TestCatalogNoticeInReportAndOffModes(t *testing.T) {
	for _, c := range []struct {
		mode string
		subs []string
	}{
		{config.ExtrasReport, []string{"1 update: memory-demo", "extras setting is report"}},
		{config.ExtrasOff, []string{"Hermes lists 1 plugin update from the new catalog: memory-demo", "not checked by Safe Update", "hermes plugins update <name>", "hermes plugins auto-update on"}},
	} {
		h, _, cards := noticeHarness(t, noticeThisProfile)
		h.cfg.Extras.Plugins = c.mode
		h.con.Keys = []platform.Key{key('y')}
		if err := h.run(); err != nil {
			t.Fatalf("%s: run: %v", c.mode, err)
		}
		wantRow(t, lastCard(t, cards), "Plugins", c.subs...)
		if h.xRan("plugins", "update", "memory-demo") != 0 {
			t.Errorf("%s: updated", c.mode)
		}
		if c.mode == config.ExtrasOff && h.xRanAny("plugins", "check-updates") {
			t.Errorf("%s: the plugin check ran", c.mode)
		}
	}
}

func TestCatalogNoticeWithEveryExtraOffStillShowsARow(t *testing.T) {
	h, _, cards := noticeHarness(t, noticeThisProfile)
	h.cfg.Extras.Plugins, h.cfg.Extras.Skills, h.cfg.Extras.Tools = config.ExtrasOff, config.ExtrasOff, config.ExtrasOff
	h.cfg.Extras.StaleDays = 0
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	wantRow(t, lastCard(t, cards), "Plugins", "memory-demo", "hermes plugins update <name>")
}

func TestCatalogNoticeUnattendedNeverAsks(t *testing.T) {
	h, _, cards := noticeHarness(t, noticeThisProfile)
	if err := h.run("unattended"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if h.xRan("plugins", "update", "memory-demo") != 0 {
		t.Error("unattended run updated a plugin")
	}
	wantRow(t, lastCard(t, cards), "Plugins", "1 update: memory-demo", "not applied")
}

func TestCatalogNoticeOnAnUnverifiedUpdateStillShows(t *testing.T) {
	h, _, cards := noticeHarness(t, noticeThisProfile)
	h.verifier = fakeVerifier{ok: false, out: "desktop build missing"}
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); !errors.Is(err, apperr.ErrUpdateProblem) {
		t.Fatalf("err = %v", err)
	}
	wantRow(t, lastCard(t, cards), "Plugins", "Hermes lists 1 plugin update from the new catalog: memory-demo")
	if h.xRanAny("plugins", "check-updates") {
		t.Error("extras ran after an unverified update")
	}
}

// The notice lines are hidden from the screen only when they parse.
func TestOutputFilterHidesTheNoticeOnly(t *testing.T) {
	f := &outputFilter{}
	lines := strings.Split(strings.TrimRight(fixture(t, "update-catalog-notice.txt"), "\n"), "\n")
	var shown []string
	for _, l := range lines {
		if s := f.display(l); s != "" {
			shown = append(shown, s)
		}
	}
	got := strings.Join(shown, "\n")
	for _, bad := range []string{"plugin update(s)", "apply: hermes plugins update"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q still shown:\n%s", bad, got)
		}
	}
	if !strings.Contains(got, "Update complete!") || !strings.Contains(got, "Building the TUI") {
		t.Errorf("other lines were hidden:\n%s", got)
	}
	// an apply line that does not follow a notice (or a notice that does not
	// parse) is left alone
	g := &outputFilter{}
	for _, l := range []string{"    apply: hermes plugins update", "  → 2 plugin update(s) from the new catalog in this profile: bad;name"} {
		if g.display(l) == "" {
			t.Errorf("%q hidden", l)
		}
	}
}

// ---- 3. plugins pinned with --ref ----

func pinHarness(t *testing.T, record string) (*harness, *xs, *[]ui.Card) {
	t.Helper()
	h, s, cards := extrasHarness(t)
	s.skills = xSkillsCurrent
	writeInstallRecord(t, h.extrasHome, record)
	return h, s, cards
}

func fixtureBody(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "hermes", "testdata", "contracts", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const xNotesUpdate = `{"name": "notes-demo", "class": "catalog", "current": "1111111111111111", "latest": "89abcdef01234567", "update_available": true, "needs_fixing": null, "reason": ""}`
const xMemoryUpdate = `{"name": "memory-demo", "class": "catalog", "current": "0123456789abcdef", "latest": "89abcdef01234567", "update_available": true, "needs_fixing": null, "reason": ""}`

func TestRefPinnedPluginIsNeverOfferedForBothHermesRecordFormats(t *testing.T) {
	for _, fx := range []string{"plugins-install-metadata-v0.22.7.json", "plugins-install-metadata-v0.23.0.json"} {
		h, s, cards := pinHarness(t, fixtureBody(t, fx))
		s.plugins = "[" + xMemoryUpdate + "," + xNotesUpdate + "]"
		h.con.Keys = []platform.Key{key('y'), key('x')} // memory-demo yes; a second question would take the x
		if err := h.run(); err != nil {
			t.Fatalf("%s: run: %v\n%s", fx, err, h.output())
		}
		if h.xRan("plugins", "update", "notes-demo") != 0 {
			t.Errorf("%s: the --ref pinned plugin was updated", fx)
		}
		if strings.Contains(h.output(), "Update notes-demo") {
			t.Errorf("%s: asked about the pinned plugin", fx)
		}
		if h.xRan("plugins", "update", "memory-demo") != 1 {
			t.Errorf("%s: the unpinned plugin was not updated: %v", fx, h.calls())
		}
		wantRow(t, lastCard(t, cards), "Plugins", "1 of 1 updated: memory-demo",
			"notes-demo pinned to 89abcdef (not updated; hermes plugins update notes-demo moves it off the pin)")
	}
}

func TestRefPinnedOnlyShowsTheRowAndNeverAsks(t *testing.T) {
	h, s, cards := pinHarness(t, fixtureBody(t, "plugins-install-metadata-v0.23.0.json"))
	s.plugins = "[" + xNotesUpdate + "]"
	h.con.Keys = []platform.Key{key('x')}
	if err := h.run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	r := wantRow(t, lastCard(t, cards), "Plugins", "notes-demo pinned to 89abcdef (not updated; hermes plugins update notes-demo moves it off the pin)")
	if strings.Contains(r.Value, "up to date") || r.Verdict == nil || *r.Verdict != ui.VerdictAttention {
		t.Errorf("row %+v", r)
	}
	if h.xRan("plugins", "update", "notes-demo") != 0 || strings.Contains(h.output(), "Update notes-demo") {
		t.Error("a --ref pinned plugin was offered")
	}
}

func TestUnreadableOrUnknownInstallRecordIsSkippedWithANote(t *testing.T) {
	for _, c := range []struct{ name, body, note string }{
		{"not JSON", `{{{`, "the install record is not a JSON object"},
		{"no record for the plugin", `{"other": {}}`, "the plugin has no install record"},
		{"older format", `{"memory-demo": {"catalog_name": "memory-demo", "pinned": true, "revision": "abc"}}`, "older package-manager format"},
	} {
		h, s, cards := pinHarness(t, c.body)
		s.plugins = "[" + xMemoryUpdate + "]"
		h.con.Keys = []platform.Key{key('x')}
		if err := h.run(); err != nil {
			t.Fatalf("%s: run: %v", c.name, err)
		}
		wantRow(t, lastCard(t, cards), "Plugins", "memory-demo has an update, but its pin state is unknown (", c.note, "was not touched")
		if h.xRan("plugins", "update", "memory-demo") != 0 || strings.Contains(h.output(), "Update memory-demo") {
			t.Errorf("%s: touched", c.name)
		}
	}
}

func TestMissingInstallRecordFileIsUnknownNotUnpinned(t *testing.T) {
	h, s, cards := pinHarness(t, "{}")
	if err := os.Remove(filepath.Join(h.extrasHome, "plugins", ".install-metadata.json")); err != nil {
		t.Fatal(err)
	}
	s.plugins = "[" + xMemoryUpdate + "]"
	h.con.Keys = []platform.Key{key('x')}
	if err := h.run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	wantRow(t, lastCard(t, cards), "Plugins", "pin state is unknown", "does not exist")
	if h.xRan("plugins", "update", "memory-demo") != 0 {
		t.Error("updated without a record")
	}
}

func TestRefPinnedInUnattendedAndReportModes(t *testing.T) {
	for _, flag := range []string{"unattended", ""} {
		h, s, cards := pinHarness(t, fixtureBody(t, "plugins-install-metadata-v0.23.0.json"))
		s.plugins = "[" + xMemoryUpdate + "," + xNotesUpdate + "]"
		if flag == "" {
			h.cfg.Extras.Plugins = config.ExtrasReport
			h.con.Keys = []platform.Key{key('x')}
		}
		if err := h.run(flag); err != nil {
			t.Fatalf("%q: run: %v", flag, err)
		}
		wantRow(t, lastCard(t, cards), "Plugins", "1 update: memory-demo", "not applied", "notes-demo pinned to 89abcdef")
	}
}

func TestCheckModeShowsTheRefPin(t *testing.T) {
	h, s, cards := pinHarness(t, fixtureBody(t, "plugins-install-metadata-v0.23.0.json"))
	s.plugins = "[" + xNotesUpdate + "]"
	h.mode = ModeCheck
	if err := h.run(); err != nil {
		t.Fatalf("check: %v", err)
	}
	wantRow(t, lastCard(t, cards), "Plugins", "notes-demo pinned to 89abcdef")
}

// Hermes ran its "already up to date" tail, so the run is as sound as after an
// update: the extras run and ask, after the relaunch.
func TestHermesHoldRunsTheExtrasAsAfterAnUpdate(t *testing.T) {
	h, _, cards := extrasHarness(t)
	h.mainCheck = "3 commits behind origin/main"
	h.onUpdate(0, fixture(t, "update-apply-hold.txt"), false)
	h.con.Keys = []platform.Key{key('y'), key('y'), key('y')} // the close, the plugin, the skills
	if err := h.run(); err != nil {
		t.Fatalf("run: %v\n%s", err, h.output())
	}
	c := lastCard(t, cards)
	if !strings.HasPrefix(c.Headline, "Hermes did not update: it held this build") {
		t.Errorf("headline %q", c.Headline)
	}
	wantRow(t, c, "Plugins", "1 of 1 updated: memory-demo")
	wantRow(t, c, "Skills", "updated")
	if h.xRan("plugins", "update", "memory-demo") != 1 {
		t.Errorf("calls: %v", h.calls())
	}
}

// ---- F4 fixes ----

// post-relaunch hooks run on a hold (pre-close ran, Hermes was closed and
// reopened); post-update hooks do not (nothing was updated).
func TestHermesHoldRunsPostRelaunchHooksButNotPostUpdate(t *testing.T) {
	for _, mode := range [][]string{nil, {"unattended"}} {
		h, _, _ := holdUpdateHarness(t, fixture(t, "update-apply-hold.txt"), false)
		hooksOn(h)
		h.cfg.Hooks = append(h.cfg.Hooks, config.Hook{Name: "pre-close-hook", When: config.HookPreClose, Run: []string{"before.exe"}})
		h.runner.On([]string{"before.exe"}, execx.Result{Output: "ok"})
		h.con.Keys = []platform.Key{key('y')}
		if err := h.run(mode...); err != nil {
			t.Fatalf("%v: %v\n%s", mode, err, h.output())
		}
		if !ranHook(h, "before.exe") || !ranHook(h, "after-relaunch.exe") {
			t.Errorf("%v: pre-close=%v post-relaunch=%v", mode, ranHook(h, "before.exe"), ranHook(h, "after-relaunch.exe"))
		}
		if ranHook(h, "after-update.exe") {
			t.Errorf("%v: post-update ran after a hold", mode)
		}
	}
}

// The verify line does not say "new code" on a hold: nothing new was installed.
func TestHermesHoldGatewayRowSaysThisBuild(t *testing.T) {
	if got := gatewayText("0123456789abcdef", hermes.GatewayState{CodeSHA: "0123456789abcdef"}, true, true); got != "running this build 0123456789" {
		t.Errorf("held: %q", got)
	}
	if got := gatewayText("0123456789abcdef", hermes.GatewayState{CodeSHA: "0123456789abcdef"}, true, false); got != "running the new code 0123456789" {
		t.Errorf("updated: %q", got)
	}
	if got := gatewayText("0123456789abcdef", hermes.GatewayState{CodeSHA: "fedcba"}, true, true); !strings.Contains(got, "expected 0123456789") {
		t.Errorf("mismatch: %q", got)
	}
	h, _, _ := holdUpdateHarness(t, fixture(t, "update-apply-hold.txt"), false)
	if err := h.run("unattended"); err != nil {
		t.Fatalf("%v", err)
	}
	if strings.Contains(h.output(), "the new code") {
		t.Errorf("screen says new code on a hold:\n%s", h.output())
	}
}

// Hermes' stash notice replaces the "✓ Holding" line: the → line alone is the hold.
func TestHermesHoldWithAnUnrestoredStashNotice(t *testing.T) {
	out := fixture(t, "update-apply-hold-stash.txt")
	h, cards, run := holdUpdateHarness(t, out, false)
	if err := h.run("unattended"); err != nil {
		t.Fatalf("err = %v\n%s", err, h.output())
	}
	c := lastCard(t, cards)
	checkHeldCard(t, h, c)
	if (*run).Facts.HermesHeld == nil {
		t.Fatal("not taken as a hold")
	}
	wantRow(t, c, "Hermes held", "3 local modification(s)", "did NOT restore", "stash@{0}")
	// without the stash notice the row says nothing about a stash
	h2, cards2, _ := holdUpdateHarness(t, fixture(t, "update-apply-hold.txt"), false)
	if err := h2.run("unattended"); err != nil {
		t.Fatal(err)
	}
	if r, _ := rowOf(lastCard(t, cards2), "Hermes held"); strings.Contains(r.Value, "stash") {
		t.Errorf("stash in %q", r.Value)
	}
	// the → line alone still needs the usual guards: rc 0, HEAD unmoved
	h3, cards3, run3 := holdUpdateHarness(t, out, true)
	if err := h3.run("unattended"); err != nil {
		t.Fatal(err)
	}
	if (*run3).Facts.HermesHeld != nil || strings.Contains(lastCard(t, cards3).Headline, "did not update") {
		t.Error("held although HEAD moved")
	}
}

// HEAD unreadable after an explicit hold is a WARN, not only an info line.
func TestHermesHoldWithUnreadableHeadWarns(t *testing.T) {
	h, _, run := holdUpdateHarness(t, fixture(t, "update-apply-hold.txt"), false)
	var log strings.Builder
	h.logSink = &log
	out := fixture(t, "update-apply-hold.txt")
	h.update = func(int) execx.Result {
		h.repo.mu.Lock()
		h.repo.headErr = true
		h.repo.mu.Unlock()
		return execx.Result{Output: out}
	}
	_ = h.run("unattended")
	if !strings.Contains(log.String(), "WARN") || !strings.Contains(log.String(), "Hermes printed a hold but HEAD after the update could not be read; not treated as a hold") {
		t.Errorf("no WARN:\n%s", log.String())
	}
	if (*run).Facts.Warnings < 1 {
		t.Errorf("warnings = %d", (*run).Facts.Warnings)
	}
}

// The pin re-check just before the update is not a refusal: the row reads like
// the probe's pinned row and carries no "run it in a terminal" suffix; a real
// refusal keeps the suffix.
func TestPinnedAfterTheProbeShowsAsPinnedNotRefused(t *testing.T) {
	h, s, cards := extrasHarness(t)
	s.skills = xSkillsCurrent
	s.plugins = "[" + xMemoryUpdate + "]"
	// the probe saw an unpinned install; the record turns ref-held (empty
	// catalog pin) after it, before the answer is applied
	h.con.BeforeRead = func() { // the probes are done; the question is about to be answered
		writeInstallRecord(t, h.extrasHome, strings.Replace(xInstallRecordCatalog("memory-demo"), `"pin": "0123456789abcdef0123456789abcdef01234567"`, `"pin": ""`, 1))
	}
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("run: %v\n%s", err, h.output())
	}
	if h.xRan("plugins", "update", "memory-demo") != 0 {
		t.Fatalf("update ran on a pinned plugin: %v", h.calls())
	}
	r := wantRow(t, lastCard(t, cards), "Plugins", "memory-demo pinned to")
	if strings.Contains(r.Value, "in a terminal") || strings.Contains(r.Value, "NOT updated or needs attention") {
		t.Errorf("row reads like a refusal: %q", r.Value)
	}
	if r.Verdict == nil || *r.Verdict == ui.VerdictBad {
		t.Errorf("verdict %v", r.Verdict)
	}
}

func TestRealRefusalKeepsTheTerminalSuffix(t *testing.T) {
	h, s, cards := extrasHarness(t)
	s.skills = xSkillsCurrent
	s.updateRes = func(*harness) execx.Result {
		return execx.Result{Code: 1, Output: "Blocked by scan\n"}
	}
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatalf("run: %v\n%s", err, h.output())
	}
	wantRow(t, lastCard(t, cards), "Plugins", "run `hermes plugins update memory-demo` in a terminal")
}
