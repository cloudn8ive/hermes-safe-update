package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/apperr"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
)

// v0.7.0 extras: plugins, hub skills and tools after the Hermes verdict,
// through the real wiring, the real extras service and the real renderers.

const (
	xPlugins = `[
  {"name": "memory-demo", "class": "catalog", "current": "0123456789abcdef0123456789abcdef01234567", "latest": "89abcdef0123456789abcdef0123456789abcdef", "update_available": true, "needs_fixing": null, "min_hermes": null, "reason": ""},
  {"name": "by-hand", "class": "manual", "current": null, "latest": null, "update_available": null, "needs_fixing": null, "min_hermes": null, "reason": "no provenance; not auto-updatable"}
]`
	xSkillsUpd = "┌──┐\n│ Name │ Source │ Status │\n├──┤\n│ demo-skill │ official │ update_available │\n└──┘\n1 update(s) available across 1 checked skill(s)\n"
	xConfig0   = "memory:\n  provider: 'memory-demo'\nplugins:\n  enabled:\n    - memory-demo\n"
)

// ranH: a `hermes <tail...>` command ran (the launcher is argv[0]).
func (w *e2eWorld) ranH(tail ...string) bool { return w.ran(append([]string{w.launcher}, tail...)...) }

func (w *e2eWorld) extrasWorld() {
	w.extras = true
	w.xConfig = xConfig0
	w.xAnswers = map[string]execx.Result{
		"plugins check-updates --json": {Output: xPlugins},
		"skills check":                 {Output: xSkillsUpd},
		"pm doctor":                    {Code: 1, Output: "✓ node 1.2.3\n✗ git: realized bytes do not match recorded digest\n"},
		"pm update --check":            {Code: 1, Output: "node 1.2.3 → 1.3.0\ngit 2.53.0+3 → 2.56.0+4\n"},
		"plugins update memory-demo":   {Output: "✓ Plugin memory-demo updated.\n"},
		"skills update":                {Output: "Updating: demo-skill\nUpdated 1 skill(s).\n"},
	}
	w.xHooks = map[string]func(){}
	dir := filepath.Join(w.home, "plugins", "memory-demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		w.t.Fatal(err)
	}
	writeFile(w.t, filepath.Join(dir, "plugin.yaml"), "name: memory-demo\nversion: 1\n")
	// Hermes' install record of a catalog plugin at its reviewed pin (the shape of v0.22.7 and v0.23.0)
	writeFile(w.t, filepath.Join(w.home, "plugins", ".install-metadata.json"), `{"memory-demo": {"pinned": true, "revision": "0123456789abcdef0123456789abcdef01234567", "source": "https://example.invalid/memory-demo", "catalog": {"name": "memory-demo", "pin": "0123456789abcdef0123456789abcdef01234567", "repo": "https://example.invalid/memory-demo", "sha": "0123456789abcdef0123456789abcdef01234567", "tier": "community"}}}`)
	w.yaml += "extras:\n  plugins: ask\n  skills: ask\n  tools: report\n  stale_days: 0\n"
}

func TestE2EExtrasCheckListsAndChangesNothing(t *testing.T) {
	modes(t, func(t *testing.T, vt bool) {
		w := newWorld(t)
		w.vt = vt
		w.extrasWorld()
		before := w.storeBytes()
		w.cli("check")
		w.transcript("extras-check")
		w.wantCode(apperr.OK)
		w.wantScreen("Update available. Hermes was not touched.",
			"1 update: memory-demo", "check only", "1 update: demo-skill",
			"git: files changed since install", "2 newer: node 1.2.3", "listed only")
		w.nothingChanged(before)
		if w.ranH("plugins", "update") || w.ranH("skills", "update") {
			t.Error("check updated a plugin or a skill")
		}
		if w.con.prompts != 0 {
			t.Errorf("check asked %d question(s)", w.con.prompts)
		}
	})
}

func TestE2EExtrasUpToDateAttendedUpdatesPluginAndSkills(t *testing.T) {
	w := newWorld(t)
	w.vt = true
	w.apiStatus = "identical"
	w.extrasWorld()
	// one
	// question per plugin, the one for the skills, then the repair offer (any other key)
	w.con.script(conStep{key: 'y'}, conStep{key: 'y'}, conStep{key: 'x'})
	w.cli()
	w.transcript("extras-up-to-date-attended")
	w.wantCode(apperr.OK)
	w.wantScreen("Hermes is up to date", "Update memory-demo (your memory provider)", "[y/N]", "1 of 1 updated: memory-demo",
		"1 update: demo-skill", "updated", "git: files changed since install")
	if !w.ranH("plugins", "update", "memory-demo") || !w.ranH("skills", "update") {
		t.Errorf("updates did not run: %v", w.Calls())
	}
	if _, err := os.Stat(filepath.Join(w.home, "safe-update-extras-backups")); err == nil {
		t.Error("Safe Update wrote a backup folder: it writes no file around a plugin update")
	}
	if w.ranH("pm", "update") {
		t.Error("the network check of tool versions ran in a normal run")
	}
	if len(w.xNoKill) != 0 {
		t.Errorf("extras commands started without a tree kill for their timeout: %v", w.xNoKill)
	}
}

func TestE2EExtrasUnattendedReportsOnly(t *testing.T) {
	w := newWorld(t)
	w.apiStatus = "identical"
	w.extrasWorld()
	w.cli("--plain", "--unattended")
	w.transcript("extras-unattended")
	w.wantCode(apperr.OK)
	w.wantScreen("1 update: memory-demo", "no one to ask", "1 update: demo-skill")
	if w.ranH("plugins", "update") || w.ranH("skills", "update") {
		t.Error("an unattended run updated something")
	}
}

const (
	xPluginsCurrent = `[{"name": "memory-demo", "class": "catalog", "current": "0123456789abcdef", "latest": "0123456789abcdef", "update_available": false, "needs_fixing": null, "min_hermes": null, "reason": ""}]`
	xSkillsCurrent  = "┌──┐\n│ Name │ Source │ Status │\n├──┤\n│ demo-skill │ official │ up_to_date │\n└──┘\n0 update(s) available across 1 checked skill(s)\n"
)

// guardWorld is an update run (3 new commits) where the dependency rebuild
// inside `hermes update` switches memory-demo off and clears memory.provider,
// as on 22 Oct. Hermes' enable / config set put it back in the attended run.
func (w *e2eWorld) guardWorld() {
	w.extrasWorld()
	w.xAnswers["plugins check-updates --json"] = execx.Result{Output: xPluginsCurrent}
	w.xAnswers["skills check"] = execx.Result{Output: xSkillsCurrent}
	cfg := filepath.Join(w.home, "config.yaml")
	rewrite := func(tail string) {
		b, _ := os.ReadFile(cfg)
		s := string(b)
		writeFile(w.t, cfg, s[:strings.Index(s, "memory:")]+tail)
	}
	w.xHooks["update"] = func() {
		rewrite("memory:\n  provider: ''\nplugins:\n  enabled:\n    - memory-demo\n  disabled:\n    - memory-demo\n")
	}
	w.xAnswers["plugins enable memory-demo"] = execx.Result{Output: "Plugin memory-demo enabled.\n"}
	w.xHooks["plugins enable memory-demo"] = func() {
		rewrite("memory:\n  provider: ''\nplugins:\n  enabled:\n    - memory-demo\n")
	}
	w.xAnswers["config set memory.provider memory-demo"] = execx.Result{Output: "set\n"}
	w.xHooks["config set memory.provider memory-demo"] = func() { rewrite(strings.TrimPrefix(xConfig0, "")) }
}

func TestE2EExtrasGuardPutsAnEvictedPluginBack(t *testing.T) {
	w := newWorld(t)
	w.vt = true
	w.guardWorld()
	w.con.script(conStep{key: 'y'}, conStep{key: 'y'}) // the update, putting the plugin back
	w.cli()
	w.transcript("extras-guard-put-back")
	w.wantCode(apperr.OK)
	w.wantScreen("Hermes is updated and verified", "Hermes switched off memory-demo during the update (memory provider cleared)",
		"put back", "memory-demo enabled", "memory.provider set to memory-demo")
	if !w.ranH("plugins", "enable", "memory-demo") || !w.ranH("config", "set", "memory.provider", "memory-demo") {
		t.Errorf("Hermes' own commands did not run: %v", w.Calls())
	}
	if b, _ := os.ReadFile(filepath.Join(w.home, "config.yaml")); !strings.Contains(string(b), "provider: 'memory-demo'") || strings.Contains(string(b), "disabled:\n    - memory-demo") {
		t.Errorf("config not back:\n%s", b)
	}
	if len(w.xNoKill) != 0 {
		t.Errorf("extras commands started without a tree kill: %v", w.xNoKill)
	}
}

func TestE2EExtrasGuardUnattendedReportsAndNamesTheCommands(t *testing.T) {
	w := newWorld(t)
	w.guardWorld()
	w.cli("--plain", "--unattended")
	w.transcript("extras-guard-unattended")
	w.wantCode(apperr.OK)
	w.wantScreen("Hermes switched off memory-demo during the update (memory provider cleared)", "not put back (no one to ask)",
		"hermes plugins enable memory-demo, then hermes config set memory.provider memory-demo")
	if w.ranH("plugins", "enable") || w.ranH("config", "set") {
		t.Error("an unattended run put a plugin back")
	}
}

// A timeout of an extras command stops the whole tree (S4): the production
// hook goes to the platform's tree kill with the child's pid.
func TestTreeKillerStopsTheWholeTreeOfTheChild(t *testing.T) {
	plat := platform.NewFake()
	procs := plat.Procs.(*platform.FakeProcs)
	procs.Add(platform.Process{PID: 4242, Name: "hermes.exe"})
	procs.Add(platform.Process{PID: 4243, PPID: 4242, Name: "python.exe"})
	if err := treeKiller(procs)(4242); err != nil {
		t.Fatal(err)
	}
	if k := procs.Killed(); len(k) != 1 || k[0] != 4242 {
		t.Errorf("killed = %v, want the child's pid through KillTree", k)
	}
}

// v0.8.0: the order of the questions on an attended up-to-date run, in the plain
// transcript: verdict, plugin, skills, and only then the repair offer.
func TestE2EExtrasQuestionsComeBeforeTheRepairOffer(t *testing.T) {
	w := newWorld(t)
	w.vt = true
	w.apiStatus = "identical"
	w.extrasWorld()
	w.con.script(conStep{key: 'n'}, conStep{key: 'n'}, conStep{key: 'x'})
	w.cli("--plain")
	w.wantCode(apperr.OK)
	s := w.screen()
	at := -1
	for _, sub := range []string{"Nothing to update", "== Plugins, skills and tools", "Update memory-demo (your memory provider)",
		"waiting for hub skills", "Press R to repair this install", "Any other key ends the run without repairing"} {
		i := strings.Index(s, sub)
		if i < 0 || i < at {
			t.Fatalf("%q missing or out of order (at %d, previous %d):\n%s", sub, i, at, s)
		}
		at = i
	}
	if strings.Contains(s, "exits without changes") {
		t.Errorf("stale offer wording:\n%s", s)
	}
	if w.ranH("plugins", "update") || w.ranH("skills", "update") || w.ranH("update", "--yes") {
		t.Errorf("something ran without a yes: %v", w.Calls())
	}
}
