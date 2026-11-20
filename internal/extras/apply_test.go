package extras

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
)

const baseConfig = `memory:
  provider: 'memory-demo'
plugins:
  enabled:
    - memory-demo
    - notes-demo
  disabled:
    - old-off
model: x
`

// evictedConfig is what the dependency rebuild of 22 Oct left: memory-demo
// switched off, memory.provider cleared.
const evictedConfig = `memory:
  provider: ''
plugins:
  enabled:
    - memory-demo
    - notes-demo
  disabled:
    - old-off
    - memory-demo
model: x
`

type world struct {
	t     *testing.T
	home  string
	f     *execx.Fake
	svc   *Svc
	waits int
}

func newWorld(t *testing.T) *world {
	t.Helper()
	home := t.TempDir()
	w := &world{t: t, home: home, f: &execx.Fake{}}
	w.write("config.yaml", baseConfig)
	w.write("plugins/memory-demo/plugin.yaml", "name: memory-demo\nversion: 1\n")
	w.write("plugins/.install-metadata.json", installRecordCatalog("memory-demo"))
	w.svc = New(Deps{Runner: w.f, Launcher: launcher, Home: home,
		KillTree: func(int) error { return nil },
		Wait:     func(time.Duration) { w.waits++ },
		Now:      func() time.Time { return time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC) }})
	return w
}

func (w *world) read(rel string) string {
	w.t.Helper()
	b, err := os.ReadFile(filepath.Join(w.home, filepath.FromSlash(rel)))
	if err != nil {
		w.t.Fatal(err)
	}
	return string(b)
}

func (w *world) write(rel, body string) {
	w.t.Helper()
	p := filepath.Join(w.home, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) ran(tail ...string) int {
	n := 0
	for _, c := range w.f.Calls() {
		if strings.Join(c.Argv, " ") == strings.Join(argv(tail...), " ") {
			n++
		}
	}
	return n
}

// updateTo scripts `plugins update memory-demo`: Hermes publishes (it, not
// Safe Update, changes files) and leaves config.yaml as cfg ("" = untouched).
func (w *world) updateTo(out, cfg string) {
	w.f.OnFunc(argv("plugins", "update", "memory-demo"), func(execx.Cmd) execx.Result {
		w.write("plugins/memory-demo/plugin.yaml", "name: memory-demo\nversion: 2\n")
		if cfg != "" {
			w.write("config.yaml", cfg)
		}
		return execx.Result{Output: out}
	})
}

// snapshot of everything under the home that Safe Update could write.
func (w *world) tree() map[string]string {
	w.t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(w.home, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(w.home, p)
			out[rel] = string(b)
		}
		return nil
	})
	return out
}

func sameTree(a, b map[string]string) (string, bool) {
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return k, false
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			return k, false
		}
	}
	return "", true
}

// ---- the guard comparison ----

func st(provider string, enabled, disabled []string) State {
	return State{Provider: provider, Enabled: enabled, Disabled: disabled}
}

func TestCompareEviction(t *testing.T) {
	before := st("holo", []string{"holo", "notes"}, []string{"old-off"})
	cases := []struct {
		name         string
		after        State
		out          string
		evicted      string
		changed, clr bool
		provEvicted  bool
		any          bool
	}{
		{"nothing changed", before, "", "", false, false, false, false},
		{"newly disabled and provider cleared (22 Oct)", st("", []string{"holo", "notes"}, []string{"old-off", "holo"}), "", "holo", true, true, true, true},
		{"newly disabled and gone from enabled", st("", []string{"notes"}, []string{"old-off", "holo"}), "", "holo", true, true, true, true},
		{"newly disabled only", st("holo", []string{"holo", "notes"}, []string{"old-off", "notes2"}), "", "notes2", false, false, false, true},
		{"leaving enabled alone is not an eviction (Hermes leaves it in both lists)", st("holo", []string{"holo"}, []string{"old-off"}), "", "", false, false, false, false},
		{"provider changed to another", st("other", []string{"holo", "notes"}, []string{"old-off"}), "", "", true, false, false, true},
		{"provider cleared, plugin not switched off", st("", []string{"holo", "notes"}, []string{"old-off"}), "", "", true, true, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := Compare(before, c.after, c.out)
			if strings.Join(g.Evicted, ",") != c.evicted || g.ProviderChanged != c.changed || g.ProviderCleared != c.clr || g.ProviderEvicted() != c.provEvicted || g.Any() != c.any {
				t.Errorf("guard = %+v", g)
			}
		})
	}
}

func TestCompareUserDisabledBeforeTheRunIsNotAnEviction(t *testing.T) {
	// off before the run (plugins.disabled, or never enabled): the snapshot already has it
	before := st("", []string{"notes"}, []string{"holo"})
	after := st("", []string{"notes"}, []string{"holo"})
	if g := Compare(before, after, "Disabled plugin 'holo' in /h: x"); g.Any() {
		t.Errorf("a plugin the user had disabled counted as evicted: %+v", g)
	}
	// listed in both enabled and disabled before the run: still the user's state
	if g := Compare(st("", []string{"holo"}, []string{"holo"}), st("", nil, []string{"holo"}), ""); g.Any() {
		t.Errorf("enabled+disabled before the run counted: %+v", g)
	}
}

func TestCompareProviderChangedBeforeTheRunIsNotAnEviction(t *testing.T) {
	// the user switched the provider before the run: the snapshot has the new value
	before := st("mine", []string{"mine"}, nil)
	if g := Compare(before, before, ""); g.Any() {
		t.Errorf("an unchanged provider counted: %+v", g)
	}
	// no provider before: nothing to lose
	if g := Compare(st("", []string{"a"}, nil), st("", []string{"a"}, nil), ""); g.ProviderChanged {
		t.Errorf("empty provider before counted as changed: %+v", g)
	}
	if g := Compare(st("", []string{"a"}, nil), st("a", []string{"a"}, nil), ""); g.ProviderChanged {
		t.Errorf("a provider that appeared counted as changed: %+v", g)
	}
}

func TestCompareKeepsHermesReasonLines(t *testing.T) {
	// as Hermes prints it: a leading warning sign, the reason cut mid-word and with colons in it
	out := "x\n⚠ Disabled plugin 'holo' in C:\\h: the dependency environment no longer builds with it: ed to build wheel: error: boom\ny\n"
	g := Compare(st("holo", []string{"holo"}, nil), st("", nil, []string{"holo"}), out)
	if len(g.Reasons) != 1 || !strings.Contains(g.Reasons[0], "no longer builds") || !strings.Contains(g.Reasons[0], "boom") {
		t.Errorf("reasons = %v", g.Reasons)
	}
}

// ---- reading the state ----

func TestPluginStateRetriesAThenReportsAnUnreadableConfig(t *testing.T) {
	w := newWorld(t)
	w.write("config.yaml", "plugins: [unclosed\n  - x: : :\n")
	if _, err := w.svc.PluginState(context.Background()); err == nil {
		t.Fatal("an unreadable config was read")
	}
	if w.waits != stateReads-1 {
		t.Errorf("waited %d times, want %d (3 reads)", w.waits, stateReads-1)
	}
}

func TestPluginStateRecoversOnARetry(t *testing.T) {
	w := newWorld(t)
	p := filepath.Join(w.home, "config.yaml")
	good, _ := os.ReadFile(p)
	if err := os.WriteFile(p, []byte("plugins: [unclosed\n  - x: : :\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.svc.d.Wait = func(time.Duration) { w.waits++; _ = os.WriteFile(p, good, 0o644) }
	got, err := w.svc.PluginState(context.Background())
	if err != nil || got.Provider != "memory-demo" || w.waits != 1 {
		t.Fatalf("state=%+v err=%v waits=%d", got, err, w.waits)
	}
}

// ---- plugin update: Hermes does it, Safe Update only looks ----

func TestUpdatePluginSuccessWritesNothingItself(t *testing.T) {
	w := newWorld(t)
	w.updateTo("✓ Plugin memory-demo updated.\n", "")
	before := w.tree()
	o := w.svc.UpdatePlugin(context.Background(), "memory-demo")
	if o.Status != StatusUpdated || o.Bad() || len(o.Problems) != 0 {
		t.Fatalf("outcome = %+v", o)
	}
	// only what the (fake) Hermes wrote may differ: the plugin folder
	after := w.tree()
	for k, v := range after {
		if k == filepath.Join("plugins", "memory-demo", "plugin.yaml") {
			continue
		}
		if before[k] != v {
			t.Errorf("%s changed", k)
		}
	}
	if _, err := os.Stat(filepath.Join(w.home, "safe-update-extras-backups")); err == nil {
		t.Error("a backup folder was made")
	}
	for _, c := range w.f.Calls() {
		if len(c.Argv) > 2 && (c.Argv[1] == "plugins" && c.Argv[2] == "enable" || c.Argv[1] == "config") {
			t.Errorf("a repair command ran after a good update: %v", c.Argv)
		}
	}
}

func TestUpdatePluginAlreadyCurrent(t *testing.T) {
	w := newWorld(t)
	w.f.On(argv("plugins", "update", "memory-demo"), execx.Result{Output: "✓ Plugin memory-demo is already at catalog pin 0123abcd.\n"})
	if o := w.svc.UpdatePlugin(context.Background(), "memory-demo"); o.Status != StatusCurrent || o.Bad() {
		t.Fatalf("outcome = %+v", o)
	}
}

func TestUpdatePluginRefusalChangesNothing(t *testing.T) {
	w := newWorld(t)
	w.f.On(argv("plugins", "update", "memory-demo"), execx.Result{Code: 1, Output: "⚠ Security scan flagged this plugin:\nBlocked: dangerous verdict\n"})
	before := w.tree()
	o := w.svc.UpdatePlugin(context.Background(), "memory-demo")
	if o.Status != StatusRefused || !o.Bad() || !strings.Contains(o.Detail, "Blocked") {
		t.Fatalf("outcome = %+v", o)
	}
	if k, ok := sameTree(before, w.tree()); !ok {
		t.Errorf("%s changed after a refusal", k)
	}
	if w.ran("plugins", "enable", "memory-demo") != 0 {
		t.Error("enable ran after a refusal")
	}
}

func TestUpdatePluginExitCodeDecidesNotWordsInTheOutput(t *testing.T) {
	// exit 0 whose output happens to contain a refusal word is an update
	w := newWorld(t)
	w.updateTo("Error: could not fetch the changelog (ignored)\nnot applied: the optional demo\n✓ Plugin memory-demo updated.\n", "")
	if o := w.svc.UpdatePlugin(context.Background(), "memory-demo"); o.Status != StatusUpdated || o.Bad() {
		t.Fatalf("outcome = %+v", o)
	}
	// exit 1 with none of the words is still not an update
	w2 := newWorld(t)
	w2.f.On(argv("plugins", "update", "memory-demo"), execx.Result{Code: 2, Output: "something odd\n"})
	if o := w2.svc.UpdatePlugin(context.Background(), "memory-demo"); o.Status != StatusRefused {
		t.Fatalf("outcome = %+v", o)
	}
}

func TestUpdatePluginNeverPassesForceOrYes(t *testing.T) {
	w := newWorld(t)
	w.updateTo("ok\n", "")
	w.svc.UpdatePlugin(context.Background(), "memory-demo")
	for _, c := range w.f.Calls() {
		for _, a := range c.Argv {
			if a == "--force" || a == "--yes" || a == "-y" {
				t.Errorf("passed %s: %v", a, c.Argv)
			}
		}
	}
}

func TestUpdatePluginUnsafeNameRunsNothing(t *testing.T) {
	w := newWorld(t)
	if o := w.svc.UpdatePlugin(context.Background(), "../x"); o.Status != StatusFailed || len(w.f.Calls()) != 0 {
		t.Errorf("outcome = %+v calls=%v", o, w.f.Calls())
	}
}

func TestUpdatePluginTimeoutAdvice(t *testing.T) {
	w := newWorld(t)
	w.f.On(argv("plugins", "update", "memory-demo"), execx.Result{Code: execx.CodeTimeout, TimedOut: true, Output: "timed out after 15m0s"})
	o := w.svc.UpdatePlugin(context.Background(), "memory-demo")
	if o.Status != StatusFailed || !strings.Contains(o.Detail, "timed out") || !strings.Contains(o.Detail, "plugins check-updates") {
		t.Fatalf("outcome = %+v", o)
	}
}

func TestUpdatePluginReportsWhatItFoundAndRewritesNothing(t *testing.T) {
	cases := []struct {
		name, cfg string
		want      []string
	}{
		{"switched off and provider cleared", evictedConfig, []string{"memory-demo is switched off now", "hermes plugins enable memory-demo", "memory.provider changed", "hermes config set memory.provider memory-demo"}},
		{"another plugin switched off meanwhile", strings.Replace(baseConfig, "    - old-off\n", "    - old-off\n    - notes-demo\n", 1), []string{"notes-demo was switched off meanwhile", "hermes plugins enable notes-demo"}},
		{"provider changed", strings.Replace(baseConfig, "provider: 'memory-demo'", "provider: 'zzz'", 1), []string{"memory.provider changed from \"memory-demo\" to \"zzz\""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.updateTo("Plugin memory-demo updated.\n", c.cfg)
			o := w.svc.UpdatePlugin(context.Background(), "memory-demo")
			if o.Status != StatusUpdated || !o.Bad() {
				t.Fatalf("outcome = %+v", o)
			}
			for _, s := range c.want {
				if !strings.Contains(o.Detail, s) {
					t.Errorf("detail lacks %q: %s", s, o.Detail)
				}
			}
			if w.read("config.yaml") != c.cfg {
				t.Error("config.yaml was rewritten")
			}
			for _, call := range w.f.Calls() {
				if len(call.Argv) > 2 && (call.Argv[2] == "enable" || call.Argv[1] == "config") {
					t.Errorf("a repair command ran: %v", call.Argv)
				}
			}
		})
	}
}

func TestUpdatePluginUnreadableConfigAfterwardsIsCannotVerifyNotARollback(t *testing.T) {
	w := newWorld(t)
	w.updateTo("Plugin memory-demo updated.\n", "plugins: [unclosed\n  - x: : :\n")
	o := w.svc.UpdatePlugin(context.Background(), "memory-demo")
	if o.Status != StatusUpdated || len(o.Problems) != 1 || !strings.Contains(o.Problems[0], "could not be read afterwards") || !strings.Contains(o.Problems[0], "hermes plugins list") {
		t.Fatalf("outcome = %+v", o)
	}
	// 1 read before + 3 reads after = 2 waits after (the first read of the "before" works)
	if w.waits != stateReads-1 {
		t.Errorf("waits = %d", w.waits)
	}
	if w.read("plugins/memory-demo/plugin.yaml") != "name: memory-demo\nversion: 2\n" {
		t.Error("the updated plugin folder was touched")
	}
}

func TestUpdatePluginWithoutAStateBeforeStillUpdatesAndChecksWhatItCan(t *testing.T) {
	w := newWorld(t)
	w.write("config.yaml", "plugins: [unclosed\n  - x: : :\n")
	w.f.OnFunc(argv("plugins", "update", "memory-demo"), func(execx.Cmd) execx.Result {
		w.write("config.yaml", evictedConfig)
		return execx.Result{Output: "Plugin memory-demo updated.\n"}
	})
	o := w.svc.UpdatePlugin(context.Background(), "memory-demo")
	if o.Status != StatusUpdated || len(o.Problems) != 1 || !strings.Contains(o.Problems[0], "is switched off") {
		t.Fatalf("outcome = %+v", o)
	}
}

// ---- enable and set provider: Hermes' own commands ----

func TestEnablePlugin(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		w := newWorld(t)
		w.write("config.yaml", evictedConfig)
		w.f.OnFunc(argv("plugins", "enable", "memory-demo"), func(execx.Cmd) execx.Result {
			w.write("config.yaml", strings.Replace(baseConfig, "provider: 'memory-demo'", "provider: ''", 1)) // enabled, provider still cleared
			return execx.Result{Output: "enabled\n"}
		})
		if o := w.svc.EnablePlugin(context.Background(), "memory-demo"); !o.OK {
			t.Fatalf("outcome = %+v", o)
		}
	})
	t.Run("exit code", func(t *testing.T) {
		w := newWorld(t)
		w.f.On(argv("plugins", "enable", "memory-demo"), execx.Result{Code: 1, Output: "the environment no longer builds with it\n"})
		o := w.svc.EnablePlugin(context.Background(), "memory-demo")
		if o.OK || !strings.Contains(o.Detail, "no longer builds") {
			t.Fatalf("outcome = %+v", o)
		}
	})
	t.Run("exit 0 but still off", func(t *testing.T) {
		w := newWorld(t)
		w.write("config.yaml", evictedConfig)
		w.f.On(argv("plugins", "enable", "memory-demo"), execx.Result{Output: "enabled\n"})
		if o := w.svc.EnablePlugin(context.Background(), "memory-demo"); o.OK {
			t.Fatalf("outcome = %+v", o)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		w := newWorld(t)
		w.f.On(argv("plugins", "enable", "memory-demo"), execx.Result{Code: execx.CodeTimeout, TimedOut: true, Output: "timed out"})
		if o := w.svc.EnablePlugin(context.Background(), "memory-demo"); o.OK || !strings.Contains(o.Detail, "timed out") {
			t.Fatalf("outcome = %+v", o)
		}
	})
	t.Run("unsafe name", func(t *testing.T) {
		w := newWorld(t)
		if o := w.svc.EnablePlugin(context.Background(), "a b"); o.OK || len(w.f.Calls()) != 0 {
			t.Fatalf("outcome = %+v", o)
		}
	})
}

func TestSetMemoryProvider(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		w := newWorld(t)
		w.write("config.yaml", strings.Replace(baseConfig, "provider: 'memory-demo'", "provider: ''", 1))
		w.f.OnFunc(argv("config", "set", "memory.provider", "memory-demo"), func(execx.Cmd) execx.Result {
			w.write("config.yaml", baseConfig)
			return execx.Result{Output: "set\n"}
		})
		if o := w.svc.SetMemoryProvider(context.Background(), "memory-demo"); !o.OK {
			t.Fatalf("outcome = %+v", o)
		}
		if w.ran("config", "set", "memory.provider", "memory-demo") != 1 {
			t.Error("not run with the exact argv")
		}
	})
	t.Run("exit code", func(t *testing.T) {
		w := newWorld(t)
		w.f.On(argv("config", "set", "memory.provider", "memory-demo"), execx.Result{Code: 1, Output: "no\n"})
		if o := w.svc.SetMemoryProvider(context.Background(), "memory-demo"); o.OK {
			t.Fatalf("outcome = %+v", o)
		}
	})
	t.Run("exit 0 but not set", func(t *testing.T) {
		w := newWorld(t)
		w.write("config.yaml", strings.Replace(baseConfig, "provider: 'memory-demo'", "provider: ''", 1))
		w.f.On(argv("config", "set", "memory.provider", "memory-demo"), execx.Result{Output: "set\n"})
		if o := w.svc.SetMemoryProvider(context.Background(), "memory-demo"); o.OK {
			t.Fatalf("outcome = %+v", o)
		}
	})
	t.Run("unsafe name", func(t *testing.T) {
		w := newWorld(t)
		if o := w.svc.SetMemoryProvider(context.Background(), "x;y"); o.OK || len(w.f.Calls()) != 0 {
			t.Fatalf("outcome = %+v", o)
		}
	})
}

// ---- B1: a step that changes the install is not cut by the run's context ----

// ctxRunner records what each command was given.
type ctxRunner struct {
	mu   sync.Mutex
	seen []seenCmd
	res  execx.Result
}

type seenCmd struct {
	argv    []string
	timeout time.Duration
	err     error
	hasDone bool
}

func (c *ctxRunner) Run(ctx context.Context, cmd execx.Cmd) (execx.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen = append(c.seen, seenCmd{argv: cmd.Argv, timeout: cmd.Timeout, err: ctx.Err(), hasDone: ctx.Done() != nil})
	return c.res, nil
}
func (c *ctxRunner) Stream(ctx context.Context, cmd execx.Cmd, _ func(string)) (execx.Result, error) {
	return c.Run(ctx, cmd)
}

func TestChangingStepsKeepTheirOwnBudgetWhenTheRunIsCancelled(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(baseConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRecord(t, home, installRecordCatalog("memory-demo"))
	r := &ctxRunner{res: execx.Result{Output: "Plugin memory-demo updated.\n"}}
	svc := New(Deps{Runner: r, Launcher: launcher, Home: home, KillTree: func(int) error { return nil }})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Ctrl+C, or finally's deadline: the step must not be cut by it
	svc.UpdatePlugin(ctx, "memory-demo")
	svc.EnablePlugin(ctx, "memory-demo")
	svc.SetMemoryProvider(ctx, "memory-demo")
	svc.UpdateSkills(ctx)
	want := map[string]time.Duration{"update": ApplyTimeout, "enable": EnableTimeout, "set": EnableTimeout}
	if len(r.seen) != 4 {
		t.Fatalf("ran %d commands: %v", len(r.seen), r.seen)
	}
	for _, s := range r.seen {
		if s.err != nil || s.hasDone {
			t.Errorf("%v got a cancellable context (err=%v)", s.argv, s.err)
		}
		if s.timeout <= 0 {
			t.Errorf("%v has no timeout of its own", s.argv)
		}
		if w, ok := want[s.argv[2]]; ok && s.argv[1] != "skills" && s.timeout != w {
			t.Errorf("%v timeout = %v, want %v", s.argv, s.timeout, w)
		}
	}
}

func TestProbesAreStillCancellable(t *testing.T) {
	r := &ctxRunner{res: execx.Result{Output: pluginsCurrentJSON}}
	svc := New(Deps{Runner: r, Launcher: launcher, KillTree: func(int) error { return nil }})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc.Probe(ctx, Options{Plugins: true})
	if len(r.seen) != 1 || r.seen[0].err == nil {
		t.Errorf("a probe did not see the cancelled context: %+v", r.seen)
	}
}

// ---- S4: a timeout stops the whole tree ----

func TestEveryHermesChildGetsATreeKill(t *testing.T) {
	var killed []int
	f := &execx.Fake{}
	f.OnPrefix([]string{launcher}, execx.Result{Output: pluginsCurrentJSON})
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(baseConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRecord(t, home, installRecordCatalog("memory-demo"))
	svc := New(Deps{Runner: f, Launcher: launcher, Home: home, KillTree: func(pid int) error { killed = append(killed, pid); return nil }})
	ctx := context.Background()
	svc.Probe(ctx, Options{Plugins: true, Skills: true, Tools: true, PMUpdate: true})
	svc.UpdatePlugin(ctx, "memory-demo")
	svc.EnablePlugin(ctx, "memory-demo")
	svc.SetMemoryProvider(ctx, "memory-demo")
	svc.UpdateSkills(ctx)
	calls := f.Calls()
	if len(calls) < 8 {
		t.Fatalf("only %d commands ran", len(calls))
	}
	for _, c := range calls {
		if c.KillTree == nil {
			t.Errorf("%v has no tree kill: a timeout would leave its Python child running", c.Argv)
			continue
		}
		_ = c.KillTree(4242)
	}
	if len(killed) != len(calls) || killed[0] != 4242 {
		t.Errorf("tree kills = %v for %d commands", killed, len(calls))
	}
}

// v0.8.0 (F2 doubt): a Hermes apply command that hangs at a question is cut
// early; the outcome names the question and the manual command.
func TestStalledAtAPromptIsReportedWithTheManualCommand(t *testing.T) {
	res := execx.Result{Code: execx.CodeTimeout, TimedOut: true, Stalled: true, Prompt: "Grant these capabilities? [y/N]", Output: "cut: it asked a question"}
	if why := failText(res, nil); !strings.Contains(why, "needs a terminal") || !strings.Contains(why, "Grant these capabilities? [y/N]") {
		t.Errorf("failText = %q", why)
	}
	w := newWorld(t)
	w.f.On(argv("plugins", "enable", "memory-demo"), res)
	o := w.svc.EnablePlugin(context.Background(), "memory-demo")
	if o.OK || !strings.Contains(o.Detail, "hermes plugins enable memory-demo` in a terminal") {
		t.Errorf("enable: %+v", o)
	}
	for _, c := range w.f.Calls() {
		if len(c.Argv) > 2 && c.Argv[1] == "plugins" && c.Argv[2] == "enable" && c.PromptQuiet != PromptQuiet {
			t.Errorf("the enable command runs without the prompt watch: %+v", c)
		}
	}
	w2 := newWorld(t)
	w2.f.On(argv("plugins", "update", "memory-demo"), res)
	p := w2.svc.UpdatePlugin(context.Background(), "memory-demo")
	if p.Status != StatusFailed || !strings.Contains(p.Detail, "hermes plugins update memory-demo` in a terminal") {
		t.Errorf("update: %+v", p)
	}
	for _, c := range w2.f.Calls() {
		if len(c.Argv) > 2 && c.Argv[1] == "plugins" && c.Argv[2] == "update" && c.PromptQuiet != PromptQuiet {
			t.Errorf("the update command runs without the prompt watch: %+v", c)
		}
	}
}

// F3 item 4: 60 s of silence, and the cut is logged with the question.
func TestPromptQuietIsSixtySecondsAndTheCutIsLogged(t *testing.T) {
	if PromptQuiet != 60*time.Second {
		t.Errorf("PromptQuiet = %v", PromptQuiet)
	}
	w := newWorld(t)
	var buf bytes.Buffer
	w.svc.d.Log = slog.New(slog.NewTextHandler(&buf, nil))
	w.f.On(argv("plugins", "enable", "memory-demo"), execx.Result{Code: execx.CodeTimeout, TimedOut: true, Stalled: true, Prompt: "Grant these capabilities? [y/N]"})
	w.svc.EnablePlugin(context.Background(), "memory-demo")
	if !strings.Contains(buf.String(), "Grant these capabilities? [y/N]") || !strings.Contains(buf.String(), "cut after 1m0s") {
		t.Errorf("the cut is not logged with the prompt: %s", buf.String())
	}
}

// writeRecord puts body in plugins/.install-metadata.json under home.
func writeRecord(t *testing.T, home, body string) {
	t.Helper()
	p := filepath.Join(home, "plugins", ".install-metadata.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// installRecordCatalog is the install record of plugins installed from the
// catalog at their reviewed pin (the shape Hermes v0.22.7 and v0.23.0 write).
func installRecordCatalog(names ...string) string {
	var parts []string
	for _, n := range names {
		parts = append(parts, fmt.Sprintf(`%q: {"pinned": true, "revision": "0123456789abcdef0123456789abcdef01234567", "source": "https://example.invalid/%s", "catalog": {"name": %q, "pin": "0123456789abcdef0123456789abcdef01234567", "repo": "https://example.invalid/%s", "sha": "0123456789abcdef0123456789abcdef01234567", "tier": "community"}}`, n, n, n, n))
	}
	return "{" + strings.Join(parts, ",") + "}"
}
