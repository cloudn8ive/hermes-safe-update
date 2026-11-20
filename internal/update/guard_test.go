package update

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/extras"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/platform"
	"github.com/cloudn8ive/hermes-safe-update/internal/ui"
)

// The plugin guard (the eviction check around the Hermes update), the apply
// gate of the extras stage (S1), per-plugin questions (S6) and the apply
// budget (B1).

const (
	// the real 22 Oct state: Hermes added the plugin to plugins.disabled and left it in plugins.enabled
	xEvictedConfig = "memory:\n  provider: ''\nplugins:\n  enabled:\n    - memory-demo\n  disabled:\n    - memory-demo\n"
	// enable re-admits the plugin but leaves memory.provider cleared (as Hermes does)
	xEnabledConfig = "memory:\n  provider: ''\nplugins:\n  enabled:\n    - memory-demo\n  disabled: []\n"
	xEvictNotice   = "⚠ Disabled plugin 'memory-demo' in /h: the dependency environment no longer builds with it"
)

func (h *harness) cfgPath() string { return filepath.Join(h.extrasHome, "config.yaml") }

func (h *harness) setCfg(body string) {
	h.t.Helper()
	if err := os.WriteFile(h.cfgPath(), []byte(body), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) cfg0() string {
	h.t.Helper()
	b, err := os.ReadFile(h.cfgPath())
	if err != nil {
		h.t.Fatal(err)
	}
	return string(b)
}

// updateLeaves: the Hermes update succeeds, advances HEAD and leaves
// config.yaml as cfg; the output has the lines.
func (h *harness) updateLeaves(cfg string, lines ...string) {
	h.update = func(int) execx.Result {
		h.advance()
		h.setCfg(cfg)
		out := "→ Fetching updates\nInstalling Python dependencies…\nBuilding desktop packaged app…\n" + strings.Join(lines, "\n") + "\nUpdate complete! (v1.0 → v1.1)"
		return execx.Result{Output: out}
	}
}

// guardHarness is an attended update run on main (3 commits behind) where the
// plugins and skills are current, so only the guard asks anything.
func guardHarness(t *testing.T) (*harness, *xs, *[]ui.Card) {
	t.Helper()
	h, s, cards := extrasHarness(t)
	s.plugins, s.skills = xPluginsCurrent, xSkillsCurrent
	h.mainCheck = "3 commits behind origin/main"
	return h, s, cards
}

// enableScript scripts `plugins enable memory-demo` and `config set
// memory.provider memory-demo`; it returns the exact commands run so far.
func (h *harness) enableScript(enableRC int) {
	h.runner.OnFunc([]string{tLauncher, "plugins", "enable", "memory-demo"}, func(execx.Cmd) execx.Result {
		if enableRC != 0 {
			return execx.Result{Code: enableRC, Output: "the dependency environment still does not build with memory-demo\n"}
		}
		h.setCfg(xEnabledConfig)
		return execx.Result{Output: "Plugin memory-demo enabled.\n"}
	})
	h.runner.OnFunc([]string{tLauncher, "config", "set", "memory.provider", "memory-demo"}, func(execx.Cmd) execx.Result {
		h.setCfg(xBaseConfig)
		return execx.Result{Output: "set\n"}
	})
}

// calls: the `hermes ...` commands run, without the launcher.
func (h *harness) calls() []string {
	var out []string
	for _, c := range h.runner.Calls() {
		if len(c.Argv) > 0 && c.Argv[0] == tLauncher {
			out = append(out, strings.Join(c.Argv[1:], " "))
		}
	}
	return out
}

func (h *harness) idx(cmd string) int {
	for i, c := range h.calls() {
		if c == cmd || strings.HasPrefix(c, cmd+" ") {
			return i
		}
	}
	return -1
}

func TestGuardEvictionAttendedPutBack(t *testing.T) {
	h, _, cards := guardHarness(t)
	h.updateLeaves(xEvictedConfig, xEvictNotice)
	h.enableScript(0)
	h.con.Keys = []platform.Key{key('y'), key('y')} // the update, putting it back
	if err := h.run(); err != nil {
		t.Fatalf("run: %v\n%s", err, h.output())
	}
	u, e, c := h.idx("update --yes"), h.idx("plugins enable memory-demo"), h.idx("config set memory.provider memory-demo")
	if !(u >= 0 && u < e && e < c) {
		t.Fatalf("order update=%d enable=%d config set=%d: %v", u, e, c, h.calls())
	}
	card := lastCard(t, cards)
	r := wantRow(t, card, "Plugins", "Hermes switched off memory-demo during the update (memory provider cleared)", "put back", "memory-demo enabled", "memory.provider set to memory-demo")
	if r.Verdict == nil || *r.Verdict != ui.VerdictGood {
		t.Errorf("verdict = %v", r.Verdict)
	}
	if strings.Contains(r.Value, "Hermes cleared memory.provider") {
		t.Errorf("the provider is said twice: %q", r.Value)
	}
	wantRow(t, card, "Warnings")
	h.mustContain("WARN Hermes switched off memory-demo during the update")
	if h.cfg0() != xBaseConfig {
		t.Errorf("config:\n%s", h.cfg0())
	}
	if len(h.con.Keys) != 0 {
		t.Errorf("%d key(s) left", len(h.con.Keys))
	}
	h.mustContain("Press Y to put it back", "`hermes plugins enable memory-demo`", "`hermes config set memory.provider memory-demo`")
}

func TestGuardSafeUpdateNeverWritesConfigItself(t *testing.T) {
	// whatever the answer, config.yaml only changes when Hermes' own command does
	h, _, _ := guardHarness(t)
	h.updateLeaves(xEvictedConfig, xEvictNotice)
	h.runner.OnFunc([]string{tLauncher, "plugins", "enable", "memory-demo"}, func(execx.Cmd) execx.Result {
		if got := h.cfg0(); got != xEvictedConfig {
			t.Errorf("config changed before Hermes' enable ran:\n%s", got)
		}
		return execx.Result{Code: 1, Output: "does not build\n"}
	})
	h.con.Keys = []platform.Key{key('y'), key('y')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if h.cfg0() != xEvictedConfig {
		t.Errorf("config rewritten:\n%s", h.cfg0())
	}
}

func TestGuardReEnableFailsStaysAWarn(t *testing.T) {
	h, _, cards := guardHarness(t)
	h.updateLeaves(xEvictedConfig, xEvictNotice)
	h.enableScript(1)
	h.con.Keys = []platform.Key{key('y'), key('y')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if h.xRan("config", "set", "memory.provider", "memory-demo") != 0 {
		t.Error("memory.provider was set although the plugin could not be enabled")
	}
	r := wantRow(t, lastCard(t, cards), "Plugins", "Hermes switched off memory-demo during the update", "NOT fully put back", "does not build", "memory.provider left cleared", "hermes plugins enable memory-demo, then hermes config set memory.provider memory-demo")
	if r.Verdict == nil || *r.Verdict != ui.VerdictBad {
		t.Errorf("verdict = %v", r.Verdict)
	}
	h.mustContain("WARN could not put memory-demo back")
}

func TestGuardUnattendedReportsOnlyAndPrintsTheCommands(t *testing.T) {
	for _, flag := range []string{"unattended", "yes"} {
		t.Run(flag, func(t *testing.T) {
			h, _, cards := guardHarness(t)
			h.updateLeaves(xEvictedConfig, xEvictNotice)
			h.enableScript(0)
			if err := h.run(flag); err != nil {
				t.Fatalf("run: %v\n%s", err, h.output())
			}
			if h.xRanAny("plugins", "enable") || h.xRanAny("config", "set") {
				t.Fatalf("an unattended run put a plugin back: %v", h.calls())
			}
			r := wantRow(t, lastCard(t, cards), "Plugins", "Hermes switched off memory-demo during the update (memory provider cleared)", "not put back (no one to ask)",
				"run hermes plugins enable memory-demo, then hermes config set memory.provider memory-demo")
			if r.Verdict == nil || *r.Verdict != ui.VerdictBad {
				t.Errorf("verdict = %v", r.Verdict)
			}
			if strings.Contains(h.output(), "Press Y to put it back") {
				t.Error("an unattended run asked")
			}
			h.mustContain("WARN Hermes switched off memory-demo")
		})
	}
}

func TestGuardOnlyYPutsItBack(t *testing.T) {
	for name, keys := range map[string][]platform.Key{
		"n":       {key('y'), key('n')},
		"enter":   {key('y'), key('\r')},
		"timeout": {key('y')}, // nobody answers: the countdown ends
	} {
		t.Run(name, func(t *testing.T) {
			h, _, cards := guardHarness(t)
			h.updateLeaves(xEvictedConfig, xEvictNotice)
			h.enableScript(0)
			h.con.Keys = keys
			if err := h.run(); err != nil {
				t.Fatal(err)
			}
			if h.xRanAny("plugins", "enable") || h.xRanAny("config", "set") {
				t.Fatalf("put back without a Y: %v", h.calls())
			}
			wantRow(t, lastCard(t, cards), "Plugins", "not put back (you chose to leave it)", "hermes plugins enable memory-demo")
		})
	}
}

func TestGuardNoEvictionSaysNothing(t *testing.T) {
	h, _, cards := guardHarness(t)
	h.updateLeaves(xBaseConfig) // Hermes left the settings as they were
	h.con.Keys = []platform.Key{key('y')}
	logs := &strings.Builder{}
	h.logSink = logs
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.output(), "switched off") || h.xRanAny("plugins", "enable") {
		t.Errorf("a guard hit without a change:\n%s", h.output())
	}
	card := lastCard(t, cards)
	if r := wantRow(t, card, "Plugins", "up to date"); r.Key != "Plugins" {
		t.Errorf("row = %+v", r)
	}
	if !strings.Contains(logs.String(), "plugin guard: no plugin switched off") {
		t.Errorf("the log does not say the guard looked:\n%s", logs.String())
	}
}

func TestGuardUserDisabledBeforeTheRunIsNotAnEviction(t *testing.T) {
	h, _, _ := guardHarness(t)
	userOff := "memory:\n  provider: ''\nplugins:\n  enabled: []\n  disabled:\n    - memory-demo\n"
	h.setCfg(userOff)                     // the user switched it off before the run
	h.updateLeaves(userOff, xEvictNotice) // Hermes' notice names it, the state did not change
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.output(), "Hermes switched off") || h.xRanAny("plugins", "enable") {
		t.Errorf("a plugin the user had switched off was treated as evicted:\n%s", h.output())
	}
}

func TestGuardProviderChangedByTheUserBeforeTheRunIsNotAnEviction(t *testing.T) {
	h, _, _ := guardHarness(t)
	mine := "memory:\n  provider: 'mine'\nplugins:\n  enabled:\n    - memory-demo\n"
	h.setCfg(mine)
	h.updateLeaves(mine)
	h.con.Keys = []platform.Key{key('y')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.output(), "memory.provider") && strings.Contains(h.output(), "during the update") {
		t.Errorf("an unchanged provider was reported:\n%s", h.output())
	}
}

func TestGuardProviderChangedToAnotherByHermesIsReportedNotFixed(t *testing.T) {
	h, _, cards := guardHarness(t)
	h.updateLeaves("memory:\n  provider: 'zzz'\nplugins:\n  enabled:\n    - memory-demo\n")
	h.enableScript(0)
	h.con.Keys = []platform.Key{key('y'), key('y')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	wantRow(t, lastCard(t, cards), "Plugins", `memory.provider changed from "memory-demo" to "zzz" during the update`, "not put back", "check with hermes plugins list")
	if h.xRanAny("plugins", "enable") || h.xRanAny("config", "set") {
		t.Errorf("something was run for a provider the user may have chosen: %v", h.calls())
	}
}

func TestGuardProviderClearedWithoutAnEvictedPluginPrintsOnlyTheSetCommand(t *testing.T) {
	h, _, cards := guardHarness(t)
	h.updateLeaves("memory:\n  provider: ''\nplugins:\n  enabled:\n    - memory-demo\n")
	h.con.Keys = []platform.Key{key('y'), key('y')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	r := wantRow(t, lastCard(t, cards), "Plugins", `Hermes cleared memory.provider (it was "memory-demo")`, "hermes config set memory.provider memory-demo")
	if strings.Contains(r.Value, "plugins enable") {
		t.Errorf("enable offered for a plugin that is still enabled: %q", r.Value)
	}
}

func TestGuardAfterAnUnverifiedUpdateReportsOnly(t *testing.T) {
	h, _, cards := guardHarness(t)
	h.verifier.ok = false
	h.updateLeaves(xEvictedConfig, xEvictNotice)
	h.enableScript(0)
	h.con.Keys = []platform.Key{key('y'), key('y')}
	_ = h.run()
	if h.xRanAny("plugins", "enable") || h.xRanAny("config", "set") {
		t.Errorf("plugins were changed on a Hermes that did not verify: %v", h.calls())
	}
	if len(*cards) == 0 {
		t.Fatal("no card")
	}
	wantRow(t, lastCard(t, cards), "Plugins", "Hermes switched off memory-demo", "not put back (Hermes was not verified in this run)", "hermes plugins enable memory-demo")
}

func TestGuardSettings(t *testing.T) {
	t.Run("report never asks", func(t *testing.T) {
		h, _, cards := guardHarness(t)
		h.cfg.Extras.Plugins = config.ExtrasReport
		h.updateLeaves(xEvictedConfig, xEvictNotice)
		h.con.Keys = []platform.Key{key('y')}
		if err := h.run(); err != nil {
			t.Fatal(err)
		}
		if h.xRanAny("plugins", "enable") {
			t.Error("report mode put a plugin back")
		}
		wantRow(t, lastCard(t, cards), "Plugins", "not put back (extras.plugins is report)")
	})
	t.Run("off does not look", func(t *testing.T) {
		h, _, cards := guardHarness(t)
		h.cfg.Extras.Plugins = config.ExtrasOff
		h.updateLeaves(xEvictedConfig, xEvictNotice)
		h.con.Keys = []platform.Key{key('y')}
		if err := h.run(); err != nil {
			t.Fatal(err)
		}
		if _, ok := rowOf(lastCard(t, cards), "Plugins"); ok || strings.Contains(h.output(), "switched off") {
			t.Error("the guard ran with extras.plugins off")
		}
	})
}

func TestGuardCannotReadTheSettings(t *testing.T) {
	t.Run("before: no comparison, no failure", func(t *testing.T) {
		h, _, cards := guardHarness(t)
		h.setCfg("plugins: [unclosed\n  - x: : :\n")
		h.updateLeaves(xEvictedConfig, xEvictNotice)
		h.con.Keys = []platform.Key{key('y')}
		if err := h.run(); err != nil {
			t.Fatalf("an unreadable config must not fail the run: %v", err)
		}
		if _, ok := rowOf(lastCard(t, cards), "Plugin check"); ok {
			t.Error("a guard row without a snapshot")
		}
		if strings.Contains(h.output(), "Hermes switched off") {
			t.Error("compared against nothing")
		}
	})
	t.Run("after: says it could not check", func(t *testing.T) {
		h, _, cards := guardHarness(t)
		h.updateLeaves("plugins: [unclosed\n  - x: : :\n")
		h.con.Keys = []platform.Key{key('y')}
		if err := h.run(); err != nil {
			t.Fatal(err)
		}
		wantRow(t, lastCard(t, cards), "Plugins", "could not check whether Hermes switched a plugin off", "hermes plugins list")
	})
}

func TestGuardDoesNotRunWithoutAHermesUpdate(t *testing.T) {
	h, _, _ := extrasHarness(t) // up to date
	h.con.Keys = []platform.Key{key('n'), key('n'), key('x')}
	h.setCfg(xBaseConfig)
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.output(), "switched off") || h.xRanAny("plugins", "enable") {
		t.Error("the guard acted on a run that did not update Hermes")
	}
}

func TestGuardRowsDoNotCollide(t *testing.T) {
	// a hit and a plugin with an update: two rows, the guard's keeps "Plugins"
	h, s, cards := guardHarness(t)
	s.plugins = xPluginsUpdate
	h.updateLeaves(xEvictedConfig, xEvictNotice)
	h.enableScript(0)
	h.con.Keys = []platform.Key{key('y'), key('y'), key('n'), key('n')} // update, put back, plugin, skills
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	card := lastCard(t, cards)
	wantRow(t, card, "Plugins", "Hermes switched off memory-demo")
	wantRow(t, card, "Plugin check", "0 of 1 updated", "skipped: memory-demo")
}

func TestGuardInterruptedUpdateStillLooksAndNeverAsks(t *testing.T) {
	h, _, cards := guardHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	h.ctx = ctx
	h.update = func(int) execx.Result {
		h.setCfg(xEvictedConfig)
		cancel() // Ctrl+C in the middle of `hermes update`
		return execx.Result{Code: 1, Output: xEvictNotice + "\n"}
	}
	h.enableScript(0)
	h.con.Keys = []platform.Key{key('y'), key('y')}
	_ = h.run()
	if h.xRanAny("plugins", "enable") {
		t.Fatalf("finally put a plugin back: %v", h.calls())
	}
	if len(*cards) > 0 {
		if r, ok := rowOf(lastCard(t, cards), "Plugins"); ok {
			if !strings.Contains(r.Value, "Hermes switched off memory-demo") || !strings.Contains(r.Value, "not put back") {
				t.Errorf("row = %q", r.Value)
			}
			return
		}
	}
	t.Logf("no card row (the run ended on the cancelled context); output:\n%s", h.output())
	h.mustContain("WARN Hermes switched off memory-demo")
}

// ---- S1: apply only on a verified Hermes ----

func TestApplyBlockTable(t *testing.T) {
	cases := []struct {
		name string
		f    Facts
		want string
	}{
		{"clean up to date", Facts{}, ""},
		{"verified update", Facts{UpdateStarted: true, Verified: true}, ""},
		{"update not verified", Facts{UpdateStarted: true}, "not verified"},
		{"earlier update unfinished", Facts{Previous: &hermes.PreviousUpdate{}}, "earlier Hermes update did not finish"},
		{"finish skipped", Facts{FinishSkipped: "x"}, "did not finish"},
		{"disk too low", Facts{DiskLow: true}, "free disk space"},
		{"finished earlier update, now verified", Facts{UpdateStarted: true, Verified: true, Previous: &hermes.PreviousUpdate{}}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &Run{Facts: c.f}
			if got := r.applyBlock(); (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
				t.Errorf("applyBlock = %q, want %q", got, c.want)
			}
		})
	}
}

func TestApplyAfterTheNormalRepairOfferWasAnsweredNoStillWorks(t *testing.T) {
	// the offer on every attended up-to-date screen is not a sign of trouble
	h, _, _ := extrasHarness(t)
	h.con.Keys = []platform.Key{key('y'), key('y'), key('x')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if h.xRan("plugins", "update", "memory-demo") != 1 {
		t.Errorf("not applied on a clean up-to-date run: %v", h.calls())
	}
}

func TestApplyNotInsideAChatEvenWithoutTheEnvironmentSignal(t *testing.T) {
	// N1: the parent chain says this run is under a gateway
	h, _, cards := extrasHarness(t)
	h.chatChain(gatewayProc())
	h.con.Keys = []platform.Key{key('y'), key('y')}
	_ = h.run()
	if h.xRan("plugins", "update", "memory-demo") != 0 || h.xRan("skills", "update") != 0 {
		t.Fatalf("applied from inside a chat: %v", h.calls())
	}
	if len(*cards) > 0 {
		wantRow(t, lastCard(t, cards), "Plugins", "not applied")
	}
}

// ---- S6: one question per plugin ----

const xTwoPlugins = `[{"name": "memory-demo", "class": "catalog", "current": "0123456789abcdef", "latest": "89abcdef01234567", "update_available": true, "needs_fixing": null, "reason": ""},
 {"name": "notes-demo", "class": "catalog", "current": "1111111111111111", "latest": "2222222222222222", "update_available": true, "needs_fixing": null, "reason": ""}]`

func twoPluginsHarness(t *testing.T) (*harness, *[]ui.Card) {
	h, s, cards := extrasHarness(t)
	s.plugins, s.skills = xTwoPlugins, xSkillsCurrent
	writeInstallRecord(t, h.extrasHome, xInstallRecordCatalog("memory-demo", "notes-demo"))
	h.runner.OnFunc([]string{tLauncher, "plugins", "update", "notes-demo"}, func(execx.Cmd) execx.Result {
		return execx.Result{Output: "✓ Plugin notes-demo updated.\n"}
	})
	return h, cards
}

func TestPerPluginQuestionsNamedProviderLastEachAnswered(t *testing.T) {
	h, cards := twoPluginsHarness(t)
	// notes-demo (y), memory-demo = the provider (n), then the repair offer
	h.con.Keys = []platform.Key{key('y'), key('n'), key('x')}
	if err := h.run(); err != nil {
		t.Fatalf("run: %v\n%s", err, h.output())
	}
	if h.xRan("plugins", "update", "notes-demo") != 1 || h.xRan("plugins", "update", "memory-demo") != 0 {
		t.Fatalf("answers not per plugin: %v", h.calls())
	}
	out := h.output()
	iN, iM := strings.Index(out, "Update notes-demo "), strings.Index(out, "Update memory-demo (your memory provider) ")
	if iN < 0 || iM < 0 || iN > iM {
		t.Errorf("questions missing or the provider is not last (notes=%d memory=%d):\n%s", iN, iM, out)
	}
	if !strings.Contains(out, "01234567 -> 89abcdef") && !strings.Contains(out, "-> 89abcdef") {
		t.Errorf("the question does not show the versions:\n%s", out)
	}
	wantRow(t, lastCard(t, cards), "Plugins", "1 of 2 updated: notes-demo", "skipped: memory-demo")
}

func TestPerPluginQuestionsProviderYesOthersNo(t *testing.T) {
	h, cards := twoPluginsHarness(t)
	h.con.Keys = []platform.Key{key('n'), key('y'), key('x')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if h.xRan("plugins", "update", "notes-demo") != 0 || h.xRan("plugins", "update", "memory-demo") != 1 {
		t.Fatalf("answers not per plugin: %v", h.calls())
	}
	wantRow(t, lastCard(t, cards), "Plugins", "1 of 2 updated: memory-demo", "skipped: notes-demo")
}

func TestOneYIsNotAnAnswerForTheNextPlugin(t *testing.T) {
	h, _ := twoPluginsHarness(t)
	h.con.Keys = []platform.Key{key('y')} // only one answer: the second question times out
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if h.xRan("plugins", "update", "notes-demo")+h.xRan("plugins", "update", "memory-demo") != 1 {
		t.Errorf("one Y updated both: %v", h.calls())
	}
}

// ---- B1: the apply is not run under finally's five minutes ----

// spyService records the deadline of the context every apply is called with.
type spyService struct {
	extras.Service
	mu        sync.Mutex
	deadlines []bool
}

func (s *spyService) note(ctx context.Context) {
	_, has := ctx.Deadline()
	s.mu.Lock()
	s.deadlines = append(s.deadlines, has)
	s.mu.Unlock()
}

func (s *spyService) UpdatePlugin(ctx context.Context, n string) extras.PluginOutcome {
	s.note(ctx)
	return s.Service.UpdatePlugin(ctx, n)
}

func (s *spyService) UpdateSkills(ctx context.Context) extras.SkillsOutcome {
	s.note(ctx)
	return s.Service.UpdateSkills(ctx)
}

func TestApplyOnTheUpToDatePathHasNoFiveMinuteDeadline(t *testing.T) {
	h, _, _ := extrasHarness(t)
	spy := &spyService{}
	h.extrasWrap = func(s extras.Service) extras.Service { spy.Service = s; return spy }
	h.con.Keys = []platform.Key{key('y'), key('y'), key('x')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if len(spy.deadlines) != 2 {
		t.Fatalf("applies = %d", len(spy.deadlines))
	}
	for i, has := range spy.deadlines {
		if has {
			t.Errorf("apply %d ran under a deadline (finally's five minutes): a slow rebuild would be cut off", i)
		}
	}
}

func TestApplyNotWithTooLittleDiskOnAnUpToDateRun(t *testing.T) {
	h, _, cards := extrasHarness(t)
	h.plat.Disk.(*platform.FakeDisk).FreeBytes = 1e9
	h.con.Keys = []platform.Key{key('y'), key('y'), key('x')}
	_ = h.run()
	if h.xRan("plugins", "update", "memory-demo") != 0 || h.xRan("skills", "update") != 0 {
		t.Fatalf("applied with too little disk: %v", h.calls())
	}
	wantRow(t, lastCard(t, cards), "Plugins", "not applied: there is not enough free disk space")
	wantRow(t, lastCard(t, cards), "Skills", "not applied: there is not enough free disk space")
}

func TestGuardIgnoresAChangeOnARunThatDidNotUpdateHermes(t *testing.T) {
	// up to date: the settings change while the run goes on (a user toggle, a
	// plugin update elsewhere); with no Hermes update in this run it is not Hermes' eviction
	h, _, _ := extrasHarness(t)
	h.onCheck = func() { h.setCfg(xEvictedConfig) }
	h.con.Keys = []platform.Key{key('n'), key('n'), key('x')}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.output(), "Hermes switched off") || h.xRanAny("plugins", "enable") {
		t.Errorf("the guard acted without a Hermes update:\n%s", h.output())
	}
}

func TestPerPluginQuestionsStopWhenTheRunIsCancelled(t *testing.T) {
	h, s, _ := extrasHarness(t)
	s.plugins, s.skills = xTwoPlugins, xSkillsCurrent
	writeInstallRecord(t, h.extrasHome, xInstallRecordCatalog("memory-demo", "notes-demo"))
	ctx, cancel := context.WithCancel(context.Background())
	h.ctx = ctx
	h.runner.OnFunc([]string{tLauncher, "plugins", "update", "notes-demo"}, func(execx.Cmd) execx.Result {
		cancel() // Ctrl+C while the first plugin updates
		return execx.Result{Output: "✓ Plugin notes-demo updated.\n"}
	})
	h.con.Keys = []platform.Key{key('y'), key('y'), key('x')}
	_ = h.run()
	if h.xRan("plugins", "update", "notes-demo") != 1 || h.xRan("plugins", "update", "memory-demo") != 0 {
		t.Errorf("the second plugin was updated after Ctrl+C: %v", h.calls())
	}
}
