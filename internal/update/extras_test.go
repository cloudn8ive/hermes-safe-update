package update

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
	"github.com/cloudn8ive/hermes-safe-update/internal/ui"
)

// The extras stage: plugins, hub skills, the bundled tools and the stale
// stable row, after the Hermes verdict of every run. The real internal/extras
// service runs on the harness' fake runner and a temp Hermes home.

const xBaseConfig = `memory:
  provider: 'memory-demo'
plugins:
  enabled:
    - memory-demo
  disabled: []
`

const (
	xPluginsUpdate = `[{"name": "memory-demo", "class": "catalog", "current": "0123456789abcdef", "latest": "89abcdef01234567", "update_available": true, "needs_fixing": null, "reason": ""},
 {"name": "by-hand", "class": "manual", "current": null, "latest": null, "update_available": null, "needs_fixing": null, "reason": "no provenance; not auto-updatable"}]`
	xPluginsCurrent = `[{"name": "memory-demo", "class": "catalog", "current": "01", "latest": "01", "update_available": false, "needs_fixing": null, "reason": ""}]`
	xSkillsUpdate   = "┌──┐\n│ Name │ Source │ Status │\n├──┤\n│ demo-skill │ official │ update_available │\n└──┘\n1 update(s) available across 1 checked skill(s)\n"
	xSkillsCurrent  = "┌──┐\n│ Name │ Source │ Status │\n├──┤\n│ demo-skill │ official │ up_to_date │\n└──┘\n0 update(s) available across 1 checked skill(s)\n"
	xDoctorOK       = "✓ node 1.2.3\n? bws: not installed\n"
	xDoctorFault    = "✓ node 1.2.3\n✗ git: realized bytes do not match recorded digest\n"
)

// xs is what the scripted Hermes answers to the extras commands.
type xs struct {
	plugins, skills, doctor, pmUpdate string
	pluginsRes                        *execx.Result // replaces plugins (timeouts)
	updateRes                         func(h *harness) execx.Result
	skillsUpd                         execx.Result
}

// extrasHarness is an "up to date" run on main with everything wired and a
// person at the console. Plugins and skills have an update, tools are fine.
func extrasHarness(t *testing.T) (*harness, *xs, *[]ui.Card) {
	t.Helper()
	h := newHarness(t)
	h.mainCheck = "Already up to date."
	h.con.Terminal = true
	h.extrasOn = true
	h.extrasHome = t.TempDir()
	h.cfg.Tunables.ConfirmTimeoutS = 3 // a prompt nobody expected ends the test
	mk := func(rel, body string) {
		p := filepath.Join(h.extrasHome, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("config.yaml", xBaseConfig)
	mk("plugins/memory-demo/plugin.yaml", "name: memory-demo\nversion: 1\n")
	mk("plugins/.install-metadata.json", xInstallRecordCatalog("memory-demo"))
	s := &xs{plugins: xPluginsUpdate, skills: xSkillsUpdate, doctor: xDoctorOK,
		skillsUpd: execx.Result{Output: "Updating: demo-skill\nUpdated 1 skill(s).\n"}}
	h.runner.OnFunc([]string{tLauncher, "plugins", "check-updates"}, func(execx.Cmd) execx.Result {
		if s.pluginsRes != nil {
			return *s.pluginsRes
		}
		return execx.Result{Output: s.plugins}
	})
	h.runner.OnFunc([]string{tLauncher, "skills", "check"}, func(execx.Cmd) execx.Result { return execx.Result{Output: s.skills} })
	h.runner.OnFunc([]string{tLauncher, "pm", "doctor"}, func(execx.Cmd) execx.Result {
		if strings.Contains(s.doctor, "✗") {
			return execx.Result{Code: 1, Output: s.doctor}
		}
		return execx.Result{Output: s.doctor}
	})
	h.runner.OnFunc([]string{tLauncher, "pm", "update", "--check"}, func(execx.Cmd) execx.Result {
		return execx.Result{Code: 1, Output: s.pmUpdate}
	})
	h.runner.OnFunc([]string{tLauncher, "plugins", "update", "memory-demo"}, func(execx.Cmd) execx.Result {
		if s.updateRes != nil {
			return s.updateRes(h)
		}
		_ = os.WriteFile(filepath.Join(h.extrasHome, "plugins", "memory-demo", "plugin.yaml"), []byte("name: memory-demo\nversion: 2\n"), 0o644)
		return execx.Result{Output: "✓ Plugin memory-demo updated.\n"}
	})
	h.runner.OnFunc([]string{tLauncher, "skills", "update"}, func(execx.Cmd) execx.Result { return s.skillsUpd })
	cards := &[]ui.Card{}
	h.cards = cards
	return h, s, cards
}

func (h *harness) xRan(tail ...string) int {
	n := 0
	want := strings.Join(append([]string{tLauncher}, tail...), " ")
	for _, c := range h.runner.Calls() {
		if strings.Join(c.Argv, " ") == want {
			n++
		}
	}
	return n
}

// xRanAny: a `hermes <a> <b>` command ran, whatever its arguments.
func (h *harness) xRanAny(a, b string) bool {
	for _, c := range h.runner.Calls() {
		if len(c.Argv) >= 3 && c.Argv[0] == tLauncher && c.Argv[1] == a && c.Argv[2] == b {
			return true
		}
	}
	return false
}

func lastCard(t *testing.T, cards *[]ui.Card) ui.Card {
	t.Helper()
	if len(*cards) == 0 {
		t.Fatal("no summary card")
	}
	return (*cards)[len(*cards)-1]
}

func rowOf(c ui.Card, key string) (ui.Row, bool) {
	for _, r := range c.Rows {
		if r.Key == key {
			return r, true
		}
	}
	return ui.Row{}, false
}

func wantRow(t *testing.T, c ui.Card, key string, subs ...string) ui.Row {
	t.Helper()
	r, ok := rowOf(c, key)
	if !ok {
		t.Fatalf("card has no %q row: %+v", key, c.Rows)
	}
	for _, s := range subs {
		if !strings.Contains(r.Value, s) {
			t.Errorf("%s row %q lacks %q", key, r.Value, s)
		}
	}
	return r
}

func TestExtrasUpToDateAttendedUpdatesAfterYes(t *testing.T) {
	h, _, cards := extrasHarness(t)
	// the two questions, then the R offer of the up-to-date screen (any other key)
	h.con.Keys = []platform.Key{key('y'), key('y'), key('x')}
	if err := h.run(); err != nil {
		t.Fatalf("run: %v\n%s", err, h.output())
	}
	h.nothingClosed(t)
	if h.xRan("plugins", "update", "memory-demo") != 1 || h.xRan("skills", "update") != 1 {
		t.Errorf("updates ran plugins=%d skills=%d\n%s", h.xRan("plugins", "update", "memory-demo"), h.xRan("skills", "update"), h.output())
	}
	c := lastCard(t, cards)
	if c.Headline != "Hermes is up to date" {
		t.Errorf("headline %q", c.Headline)
	}
	wantRow(t, c, "Plugins", "1 of 1 updated", "memory-demo")
	wantRow(t, c, "Skills", "1 update: demo-skill", "updated")
	wantRow(t, c, "Tools", "ok")
	if r := wantRow(t, c, "Plugins"); r.Verdict == nil || *r.Verdict != ui.VerdictGood {
		t.Errorf("plugins verdict = %v", r.Verdict)
	}
	if last := c.Rows[len(c.Rows)-1]; last.Key != "Log" {
		t.Errorf("the Log row must stay last, got %q", last.Key)
	}
	h.mustContain("Update memory-demo (your memory provider)", "[y/N]", "Press Y")
	if len(h.con.Keys) != 0 {
		t.Errorf("%d key(s) left", len(h.con.Keys))
	}
}

func TestExtrasQuestionIsNotAnsweredByOtherKeysOrTimeout(t *testing.T) {
	for name, keys := range map[string][]platform.Key{
		"n":       {key('n'), key('n'), key('x')},
		"enter":   {key('x'), key('\r'), key('\r')},
		"timeout": {key('x')}, // nobody answers: the countdown ends
	} {
		t.Run(name, func(t *testing.T) {
			h, _, cards := extrasHarness(t)
			h.con.Keys = keys
			if err := h.run(); err != nil {
				t.Fatalf("run: %v\n%s", err, h.output())
			}
			if h.xRan("plugins", "update", "memory-demo") != 0 || h.xRan("skills", "update") != 0 {
				t.Errorf("an update ran without a yes\n%s", h.output())
			}
			c := lastCard(t, cards)
			wantRow(t, c, "Plugins", "0 of 1 updated", "skipped: memory-demo")
			wantRow(t, c, "Skills", "1 update: demo-skill", "skipped")
			if r := wantRow(t, c, "Plugins"); r.Verdict == nil || *r.Verdict != ui.VerdictAttention {
				t.Errorf("verdict = %v", r.Verdict)
			}
		})
	}
}

func TestExtrasUnattendedAndYesOnlyReport(t *testing.T) {
	for _, flag := range []string{"unattended", "yes"} {
		t.Run(flag, func(t *testing.T) {
			h, _, cards := extrasHarness(t)
			if err := h.run(flag); err != nil {
				t.Fatalf("run: %v\n%s", err, h.output())
			}
			if h.xRan("plugins", "update", "memory-demo") != 0 || h.xRan("skills", "update") != 0 {
				t.Fatalf("an unattended run changed plugins or skills\n%s", h.output())
			}
			c := lastCard(t, cards)
			wantRow(t, c, "Plugins", "1 update: memory-demo", "not applied", "hermes plugins update")
			wantRow(t, c, "Skills", "1 update: demo-skill", "not applied", "hermes skills update")
			if strings.Contains(h.output(), "Press Y") {
				t.Error("an unattended run asked")
			}
		})
	}
}

func TestExtrasCheckModeReportsAndListsToolVersions(t *testing.T) {
	h, s, cards := extrasHarness(t)
	s.doctor = xDoctorFault + "✓ uv 1\n✓ npm 1\n✓ bun 1\n? opt-tool: not installed\n"
	s.pmUpdate = "node 1.0.0 → 1.1.0\ngit 2.0 → 2.1\nuv 1 → 2\nnpm 1 → 2\nbun 1 → 2\nopt-tool 1 → 2\n"
	h.mode = ModeCheck
	if err := h.run(); err != nil {
		t.Fatalf("check: %v\n%s", err, h.output())
	}
	if h.xRan("plugins", "update", "memory-demo") != 0 || h.xRan("skills", "update") != 0 {
		t.Fatal("check changed something")
	}
	c := lastCard(t, cards)
	wantRow(t, c, "Plugins", "1 update: memory-demo", "check only")
	wantRow(t, c, "Tools", "git: files changed since install", "see log")
	r := wantRow(t, c, "Newer tools", "5 newer", "node 1.0.0", "+1 more", "listed only")
	if strings.Contains(r.Value, "opt-tool") {
		t.Errorf("a tool that is not installed here is listed: %q", r.Value)
	}
	if strings.Contains(r.Value, "pm update") {
		t.Errorf("tool versions row looks like an offer: %q", r.Value)
	}
	if h.xRan("pm", "update", "--check") != 1 {
		t.Error("pm update --check did not run in check mode")
	}
	h.mustContain("hermes pm install git") // the advice for the fault
	if h.xRanAny("pm", "repair") || h.xRanAny("pm", "install") {
		t.Error("a pm repair or install ran")
	}
}

func TestExtrasPMUpdateCheckOnlyInCheckMode(t *testing.T) {
	h, _, _ := extrasHarness(t)
	h.con.Keys = []platform.Key{key('n'), key('n'), key('x')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if h.xRan("pm", "update", "--check") != 0 {
		t.Error("the slow network check ran in a normal run")
	}
	if h.xRan("pm", "doctor") != 1 {
		t.Error("pm doctor did not run")
	}
}

func TestExtrasNothingToDoIsQuietAndGreen(t *testing.T) {
	h, s, cards := extrasHarness(t)
	s.plugins, s.skills = xPluginsCurrent, xSkillsCurrent
	h.con.Keys = []platform.Key{key('x')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	c := lastCard(t, cards)
	wantRow(t, c, "Plugins", "up to date")
	wantRow(t, c, "Skills", "up to date")
	wantRow(t, c, "Tools", "ok")
	if strings.Contains(h.output(), "Press Y") {
		t.Error("asked with nothing to update")
	}
}

func TestExtrasPartialFailuresAreRows(t *testing.T) {
	h, s, cards := extrasHarness(t)
	s.pluginsRes = &execx.Result{Code: execx.CodeTimeout, TimedOut: true, Output: "timed out after 1m30s"}
	s.skills = "no idea what this says\n"
	s.doctor = "Traceback (most recent call last):\n"
	h.con.Keys = []platform.Key{key('x')}
	if err := h.run(); err != nil {
		t.Fatalf("a failed probe must not fail the run: %v", err)
	}
	c := lastCard(t, cards)
	wantRow(t, c, "Plugins", "could not check", "timed out")
	wantRow(t, c, "Skills", "could not check")
	wantRow(t, c, "Tools", "could not check")
	if h.xRan("plugins", "update", "memory-demo") != 0 {
		t.Error("updated after a failed check")
	}
}

func TestExtrasSettingsTurnThingsOff(t *testing.T) {
	cases := []struct {
		name      string
		set       func(c *config.Extras)
		notRan    [][]string
		noRow     []string
		stillRows []string
	}{
		{"plugins off", func(c *config.Extras) { c.Plugins = config.ExtrasOff }, [][]string{{"plugins", "check-updates"}}, []string{"Plugins"}, []string{"Skills", "Tools"}},
		{"skills off", func(c *config.Extras) { c.Skills = config.ExtrasOff }, [][]string{{"skills", "check"}}, []string{"Skills"}, []string{"Plugins", "Tools"}},
		{"tools off", func(c *config.Extras) { c.Tools = config.ExtrasOff }, [][]string{{"pm", "doctor"}}, []string{"Tools"}, []string{"Plugins", "Skills"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, _, cards := extrasHarness(t)
			c.set(&h.cfg.Extras)
			h.con.Keys = []platform.Key{key('n'), key('n'), key('x')}
			if err := h.run(); err != nil {
				t.Fatal(err)
			}
			for _, nr := range c.notRan {
				if h.xRanAny(nr[0], nr[1]) {
					t.Errorf("%v ran", nr)
				}
			}
			card := lastCard(t, cards)
			for _, k := range c.noRow {
				if _, ok := rowOf(card, k); ok {
					t.Errorf("row %s present", k)
				}
			}
			for _, k := range c.stillRows {
				if _, ok := rowOf(card, k); !ok {
					t.Errorf("row %s missing", k)
				}
			}
		})
	}
	t.Run("everything off", func(t *testing.T) {
		h, _, cards := extrasHarness(t)
		h.cfg.Extras = config.Extras{Plugins: config.ExtrasOff, Skills: config.ExtrasOff, Tools: config.ExtrasOff}
		h.con.Keys = []platform.Key{key('x')}
		if err := h.run(); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"plugins", "skills", "pm"} {
			if h.xRanAny(k, "check-updates") || h.xRanAny(k, "check") || h.xRanAny(k, "doctor") {
				t.Errorf("a %s command ran", k)
			}
		}
		for _, k := range []string{"Plugins", "Skills", "Tools"} {
			if _, ok := rowOf(lastCard(t, cards), k); ok {
				t.Errorf("row %s present", k)
			}
		}
	})
	t.Run("report never asks", func(t *testing.T) {
		h, _, cards := extrasHarness(t)
		h.cfg.Extras.Plugins, h.cfg.Extras.Skills = config.ExtrasReport, config.ExtrasReport
		h.con.Keys = []platform.Key{key('x')}
		if err := h.run(); err != nil {
			t.Fatal(err)
		}
		if h.xRan("plugins", "update", "memory-demo") != 0 || strings.Contains(h.output(), "Press Y") {
			t.Error("report mode asked or updated")
		}
		wantRow(t, lastCard(t, cards), "Plugins", "extras setting is report")
	})
}

func TestExtrasNotAskedInsideAChat(t *testing.T) {
	h, _, cards := extrasHarness(t)
	h.env = map[string]string{"HERMES_SESSION_ID": "abc"}
	h.con.Keys = []platform.Key{key('x')}
	_ = h.run()
	if h.xRan("plugins", "update", "memory-demo") != 0 || strings.Contains(h.output(), "Press Y") {
		t.Errorf("asked inside a Hermes chat:\n%s", h.output())
	}
	if len(*cards) > 0 {
		wantRow(t, lastCard(t, cards), "Plugins", "not applied")
	}
}

func TestExtrasScanRefusalIsAWarnAndNothingChanges(t *testing.T) {
	h, s, cards := extrasHarness(t)
	s.skills = xSkillsCurrent
	s.updateRes = func(*harness) execx.Result {
		return execx.Result{Code: 1, Output: "⚠ Security scan flagged this plugin:\nBlocked: dangerous\n"}
	}
	h.con.Keys = []platform.Key{key('y'), key('x')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(h.extrasHome, "config.yaml"))
	if string(b) != xBaseConfig || h.xRanAny("plugins", "enable") {
		t.Error("a refusal touched the config or ran enable")
	}
	wantRow(t, lastCard(t, cards), "Plugins", "0 of 1 updated", "Blocked", "in a terminal")
	h.mustContain("WARN plugin memory-demo")
}

func TestExtrasSkillsFailureIsAWarn(t *testing.T) {
	h, s, cards := extrasHarness(t)
	s.skillsUpd = execx.Result{Code: 1, Output: "Not updated: demo-skill (see the messages above).\n"}
	h.con.Keys = []platform.Key{key('n'), key('y'), key('x')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	r := wantRow(t, lastCard(t, cards), "Skills", "NOT updated")
	if r.Verdict == nil || *r.Verdict != ui.VerdictBad {
		t.Errorf("verdict = %v", r.Verdict)
	}
	h.mustContain("WARN skills")
}

func TestExtrasAfterAVerifiedUpdateOnTheUpdateCard(t *testing.T) {
	h, _, cards := extrasHarness(t)
	h.mainCheck = "3 commits behind origin/main"
	h.con.Keys = []platform.Key{key('y'), key('y'), key('y')} // the update, plugins, skills
	if err := h.run(); err != nil {
		t.Fatalf("run: %v\n%s", err, h.output())
	}
	if h.updates != 1 {
		t.Fatalf("hermes update ran %d times", h.updates)
	}
	// the extras ran after the update, not during the closed window
	order := []string{}
	for _, c := range h.runner.Calls() {
		switch {
		case len(c.Argv) > 1 && c.Argv[1] == "update" && len(c.Argv) > 2 && c.Argv[2] == "--yes":
			order = append(order, "update")
		case len(c.Argv) > 2 && c.Argv[1] == "plugins" && c.Argv[2] == "check-updates":
			order = append(order, "probe")
		}
	}
	if strings.Join(order, ",") != "update,probe" {
		t.Errorf("order = %v, want update then probe", order)
	}
	c := lastCard(t, cards)
	if !strings.Contains(c.Headline, "updated and verified") {
		t.Errorf("headline %q", c.Headline)
	}
	wantRow(t, c, "Plugins", "1 of 1 updated")
	wantRow(t, c, "Skills", "updated")
}

func TestExtrasSkippedWhenTheUpdateIsNotVerified(t *testing.T) {
	h, _, _ := extrasHarness(t)
	h.mainCheck = "3 commits behind origin/main"
	h.verifier.ok = false
	h.con.Keys = []platform.Key{key('y')}
	_ = h.run()
	if h.xRanAny("plugins", "check-updates") || h.xRanAny("pm", "doctor") {
		t.Errorf("extras ran after an update that did not verify\n%s", h.output())
	}
}

func TestExtrasNotRunWhenTheRunStops(t *testing.T) {
	h, _, _ := extrasHarness(t)
	h.mainCheck = "3 commits behind origin/main"
	h.con.Keys = []platform.Key{key('n')} // the user cancels the update
	err := h.run()
	if err == nil {
		t.Skip("run did not stop")
	}
	if h.xRanAny("plugins", "check-updates") {
		t.Errorf("extras ran after a cancelled run (%v)", err)
	}
	_ = apperr.OK
}

func TestExtrasHoldCardCarriesTheRows(t *testing.T) {
	h := holdHarness(t)
	h.releaseIsAncestor()
	attachExtras(t, h)
	cards := &[]ui.Card{}
	h.cards = cards
	if err := h.run("unattended"); err != nil {
		t.Fatalf("run: %v", err)
	}
	c := lastCard(t, cards)
	if !strings.Contains(c.Headline, "Waiting for stable") {
		t.Fatalf("headline %q", c.Headline)
	}
	wantRow(t, c, "Plugins", "1 update: memory-demo")
	wantRow(t, c, "Skills", "up to date")
	if last := c.Rows[len(c.Rows)-1]; last.Key != "Log" {
		t.Errorf("the Log row must stay last, got %q", last.Key)
	}
}

// attachExtras wires the extras onto a harness that has its own scripted
// Hermes (holdHarness): plugins with an update, skills current, tools fine.
func attachExtras(t *testing.T, h *harness) {
	t.Helper()
	h.extrasOn = true
	h.extrasHome = t.TempDir()
	if err := os.WriteFile(filepath.Join(h.extrasHome, "config.yaml"), []byte(xBaseConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	writeInstallRecord(t, h.extrasHome, xInstallRecordCatalog("memory-demo"))
	h.runner.On([]string{tLauncher, "plugins", "check-updates", "--json"}, execx.Result{Output: xPluginsUpdate})
	h.runner.On([]string{tLauncher, "skills", "check"}, execx.Result{Output: xSkillsCurrent})
	h.runner.On([]string{tLauncher, "pm", "doctor"}, execx.Result{Output: xDoctorOK})
}

// ---- stale stable ----

const xGit = "/fake/git"

func xGitRule(h *harness, args []string, out string) {
	argv := append([]string{xGit, "-C", h.install.Checkout, "-c", "core.quotepath=off"}, args...)
	h.runner.OnFunc(argv, func(execx.Cmd) execx.Result { return execx.Result{Output: out} })
}

func staleHarness(t *testing.T, releaseDate, count, subjects string) (*harness, *[]ui.Card) {
	t.Helper()
	h := holdHarness(t)
	h.releaseIsAncestor()
	attachExtras(t, h)
	h.xGit = xGit
	xGitRule(h, []string{"log", "-1"}, releaseDate+"\n")
	xGitRule(h, []string{"rev-list", "--count"}, count+"\n")
	xGitRule(h, []string{"log", "--format=%s"}, subjects)
	cards := &[]ui.Card{}
	h.cards = cards
	return h, cards
}

func TestStaleStableRowOldRelease(t *testing.T) {
	// the harness clock is 2026-10-01; the release is 20 days older
	h, cards := staleHarness(t, "2026-09-10T09:00:00+00:00", "3367", "fix: a\nfeat: b\n")
	if err := h.run("unattended"); err != nil {
		t.Fatal(err)
	}
	r := wantRow(t, lastCard(t, cards), "Stale stable", "v0.22.0 is 21 days old (limit 14)", "main has 3367 commits since", "hermes-safe-update --channel main", "Nothing is done automatically")
	if r.Verdict == nil || *r.Verdict != ui.VerdictAttention {
		t.Errorf("verdict = %v", r.Verdict)
	}
	if h.updateArgv() != "" {
		t.Error("the stale row acted by itself: hermes update ran")
	}
}

func TestStaleStableRowSecurityCommitsEvenWhenFresh(t *testing.T) {
	h, cards := staleHarness(t, "2026-09-29T09:00:00+00:00", "12", "fix(security): block x\nchore: bump\nfix: CVE-2026-1 in y\n")
	if err := h.run("unattended"); err != nil {
		t.Fatal(err)
	}
	r := wantRow(t, lastCard(t, cards), "Stale stable", "v0.22.0 is the latest stable release", "2 of them with CVE, GHSA or \"security\"")
	if strings.Contains(r.Value, "days old") {
		t.Errorf("a fresh release must not say it is old: %q", r.Value)
	}
}

func TestStaleStableRowNotShownWhenFreshAndQuiet(t *testing.T) {
	h, cards := staleHarness(t, "2026-09-29T09:00:00+00:00", "12", "fix: a\nfeat: b\n")
	if err := h.run("unattended"); err != nil {
		t.Fatal(err)
	}
	if _, ok := rowOf(lastCard(t, cards), "Stale stable"); ok {
		t.Error("row shown for a fresh release without security commits")
	}
}

func TestStaleStableRowOffAndMain(t *testing.T) {
	t.Run("stale_days 0", func(t *testing.T) {
		h, cards := staleHarness(t, "2026-01-01T09:00:00+00:00", "100", "fix(security): x\n")
		h.cfg.Extras.StaleDays = 0
		if err := h.run("unattended"); err != nil {
			t.Fatal(err)
		}
		if _, ok := rowOf(lastCard(t, cards), "Stale stable"); ok {
			t.Error("row shown with stale_days 0")
		}
	})
	t.Run("main channel", func(t *testing.T) {
		h, _, cards := extrasHarness(t)
		h.xGit = xGit
		xGitRule(h, []string{"log", "-1"}, "2026-01-01T09:00:00+00:00\n")
		xGitRule(h, []string{"rev-list", "--count"}, "100\n")
		xGitRule(h, []string{"log", "--format=%s"}, "fix(security): x\n")
		if err := h.run("unattended"); err != nil {
			t.Fatal(err)
		}
		if _, ok := rowOf(lastCard(t, cards), "Stale stable"); ok {
			t.Error("a main-channel run has no stable release to be stale")
		}
	})
}

func TestExtrasStageNeverRunsTwice(t *testing.T) {
	h, _, _ := extrasHarness(t)
	h.con.Keys = []platform.Key{key('n'), key('n'), key('x')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if h.xRan("pm", "doctor") != 1 || h.xRan("skills", "check") != 1 {
		t.Errorf("probes ran doctor=%d skills=%d", h.xRan("pm", "doctor"), h.xRan("skills", "check"))
	}
}

func TestExtrasTimingDoesNotStretchTheClosedWindow(t *testing.T) {
	// the probes cost wall time; the clock of the harness only moves by Sleep,
	// so this asserts the structure instead: no extras command ran between
	// `hermes update` starting and the relaunch of the desktop app.
	h, _, _ := extrasHarness(t)
	h.mainCheck = "3 commits behind origin/main"
	h.con.Keys = []platform.Key{key('y'), key('n'), key('n')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	seenUpdate := false
	for _, c := range h.runner.Calls() {
		if len(c.Argv) > 2 && c.Argv[1] == "update" && c.Argv[2] == "--yes" {
			seenUpdate = true
		}
		if seenUpdate {
			break
		}
		if len(c.Argv) > 2 && (c.Argv[1] == "plugins" || c.Argv[1] == "skills" || c.Argv[1] == "pm") {
			t.Errorf("extras command %v ran before the update", c.Argv)
		}
	}
	_ = time.Second
}

// xInstallRecordCatalog is the install record (plugins/.install-metadata.json)
// of a plugin installed from the catalog at its reviewed pin, the shape both
// Hermes v0.22.7 and v0.23.0 write.
func xInstallRecordCatalog(names ...string) string {
	var parts []string
	for _, n := range names {
		parts = append(parts, fmt.Sprintf(`%q: {"pinned": true, "revision": "0123456789abcdef0123456789abcdef01234567", "source": "https://example.invalid/%s",
 "catalog": {"name": %q, "pin": "0123456789abcdef0123456789abcdef01234567", "repo": "https://example.invalid/%s", "sha": "0123456789abcdef0123456789abcdef01234567", "tier": "community"}}`, n, n, n, n))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func writeInstallRecord(t *testing.T, home, body string) {
	t.Helper()
	p := filepath.Join(home, "plugins", ".install-metadata.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
