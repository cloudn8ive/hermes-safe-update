package extras

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
)

// What happened to one plugin update.
const (
	StatusUpdated = "updated" // Hermes applied it (see Problems for what the check afterwards found)
	StatusCurrent = "current" // Hermes said it was already current
	StatusRefused = "refused" // Hermes did not apply it (scan block, consent needed, error): nothing changed
	StatusFailed  = "failed"  // could not start, timed out, ...
	StatusPinned  = "pinned"  // the pin record says leave it (or cannot be read): not run, nothing changed, not a failure
)

// PluginOutcome is the result of UpdatePlugin.
type PluginOutcome struct {
	Name   string
	Status string
	Detail string           // one line for the card
	Pin    hermes.PluginPin // set with StatusPinned
	// Problems are what the check after the update found: the plugin is no
	// longer enabled, memory.provider changed, config.yaml unreadable. Each
	// says what to run. Safe Update never rewrites anything itself.
	Problems []string
	Output   string // the command output, for the log
}

// Bad reports an outcome that needs the user's attention.
func (o PluginOutcome) Bad() bool {
	return o.Status == StatusRefused || o.Status == StatusFailed || len(o.Problems) > 0
}

// State is what a Hermes update (or a plugin change) can break in
// config.yaml: plugins.enabled, plugins.disabled and memory.provider.
type State struct {
	Enabled, Disabled []string
	Provider          string
}

// IsDisabled reports whether name is in plugins.disabled.
func (p State) IsDisabled(name string) bool { return contains(p.Disabled, name) }

// IsEnabled reports whether name is in plugins.enabled.
func (p State) IsEnabled(name string) bool { return contains(p.Enabled, name) }

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func (s *Svc) configPath() string { return filepath.Join(s.d.Home, "config.yaml") }

// readState reads the three keys once.
func (s *Svc) readState() (State, error) {
	data, err := os.ReadFile(s.configPath())
	if err != nil {
		return State{}, err
	}
	var doc struct {
		Plugins struct {
			Enabled  []string `yaml:"enabled"`
			Disabled []string `yaml:"disabled"`
		} `yaml:"plugins"`
		Memory struct {
			Provider string `yaml:"provider"`
		} `yaml:"memory"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return State{}, fmt.Errorf("config.yaml is not readable: %w", err)
	}
	return State{Enabled: doc.Plugins.Enabled, Disabled: doc.Plugins.Disabled, Provider: doc.Memory.Provider}, nil
}

const (
	stateReads = 3
	stateWait  = 300 * time.Millisecond
)

// PluginState reads plugins.enabled, plugins.disabled and memory.provider. A
// read that fails (Hermes rewriting the file, a sharing violation on Windows)
// is tried again after a short wait; after stateReads reads the error is
// returned and the caller says "cannot verify".
func (s *Svc) PluginState(ctx context.Context) (State, error) {
	var err error
	for i := 0; i < stateReads; i++ {
		if i > 0 {
			s.d.Wait(stateWait)
		}
		var st State
		if st, err = s.readState(); err == nil {
			return st, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return State{}, err
}

// ---- the eviction guard around the Hermes update ----

// Guard is what a Hermes update did to the plugin settings, found by
// comparing the state taken before it with the state after it.
type Guard struct {
	// Evicted are the plugins Hermes switched off during the update: newly in
	// plugins.disabled. Hermes adds the plugin to plugins.disabled and LEAVES
	// it in plugins.enabled (the real 22 Oct state: in both lists), so leaving
	// enabled is not the signal. A plugin that was already disabled before the
	// run is never in here.
	Evicted []string
	// Provider is memory.provider before; ProviderNow after.
	Provider, ProviderNow string
	// ProviderChanged: it was set before and is different now. Cleared: it is
	// empty now (Hermes clears it when it evicts the memory provider).
	ProviderChanged, ProviderCleared bool
	// Reasons are Hermes' own "Disabled plugin '<n>' ..." lines from the
	// update output, for the log.
	Reasons []string
}

// Any reports whether the update changed anything the guard watches.
func (g Guard) Any() bool { return len(g.Evicted) > 0 || g.ProviderChanged }

// ProviderEvicted: the memory provider was cleared and the plugin that was the
// provider is one of the evicted ones, so enabling it can put the provider back.
func (g Guard) ProviderEvicted() bool {
	return g.ProviderCleared && g.Provider != "" && contains(g.Evicted, g.Provider)
}

// Compare is the guard: before is the state taken in the pre-flight, after
// the one read once the Hermes update is over, output the update's output.
func Compare(before, after State, output string) Guard {
	g := Guard{Provider: before.Provider, ProviderNow: after.Provider}
	set := map[string]bool{}
	for _, n := range after.Disabled {
		if !before.IsDisabled(n) {
			set[n] = true
		}
	}
	for n := range set {
		g.Evicted = append(g.Evicted, n)
	}
	sort.Strings(g.Evicted)
	g.ProviderChanged = before.Provider != "" && after.Provider != before.Provider
	g.ProviderCleared = g.ProviderChanged && after.Provider == ""
	g.Reasons = hermes.EvictionLines(output)
	return g
}

// ---- plugin update ----

// atomic runs a Hermes command that changes the install (a plugin update,
// enable, config set, skills update). Hermes publishes those as one journalled
// step, so cancelling the run must not cut one in the middle: the command
// keeps its own budget, and only its own timeout (which stops the whole
// process tree, see Deps.KillTree) ends it early.
func (s *Svc) atomic(ctx context.Context, tmo time.Duration, tail ...string) (execx.Result, error) {
	res, err := s.hermesCmd(context.WithoutCancel(ctx), execx.Cmd{Timeout: tmo, PromptQuiet: PromptQuiet}, tail...)
	if res.Stalled {
		s.logf("WARN extras: hermes %s cut after %s without output: it asked %q", strings.Join(tail, " "), PromptQuiet, res.Prompt)
	}
	return res, err
}

// UpdatePlugin updates one plugin through `hermes plugins update <name>` and
// then only looks. Hermes' own plugin update is all-or-nothing (it stages the
// new tree, builds it, and publishes only on success; the live plugin is
// untouched when anything fails) and it never evicts a plugin, so the usual
// failure is "refused or failed, nothing changed". Safe Update writes no file
// here: afterwards it checks the exit code, that the plugin is still enabled
// and that memory.provider is unchanged, and says what to run when not.
func (s *Svc) UpdatePlugin(ctx context.Context, name string) PluginOutcome {
	out := PluginOutcome{Name: name}
	fail := func(status, format string, a ...any) PluginOutcome {
		out.Status, out.Detail = status, fmt.Sprintf(format, a...)
		s.logf("extras: plugin %s: %s: %s", name, status, out.Detail)
		return out
	}
	if !hermes.SafePluginName(name) {
		return fail(StatusFailed, "not a plugin name this tool will pass on")
	}
	// Defence in depth: the record is read again, because it may have
	// changed since the probe (an `install --ref` in another terminal), and
	// the named update moves a plugin off its pin.
	if pin := hermes.ReadPinStore(s.d.Home).Pin(name, ""); pin.Held() {
		out.Pin = pin
		return fail(StatusPinned, "not run: %s", pinHeldText(pin))
	}
	before, berr := s.PluginState(ctx)
	if berr != nil {
		s.logf("WARN extras: plugin %s: settings unreadable before the update: %v", name, berr)
	} else {
		s.logf("extras: plugin %s: before: enabled=%v disabled=%v memory.provider=%q", name, before.Enabled, before.Disabled, before.Provider)
	}

	res, rerr := s.atomic(ctx, ApplyTimeout, hermes.PluginUpdateArgv(name)...)
	out.Output = res.Output
	s.logf("extras: plugin %s: update exit %d (%s)", name, res.Code, tail(res.Output, 400))
	if why := failText(res, rerr); why != "" {
		switch {
		case res.Stalled:
			why += "; run `hermes plugins update " + name + "` in a terminal and answer it there"
		case res.TimedOut:
			why += "; Hermes' update is all-or-nothing, but run `hermes plugins check-updates` before anything else to see where it stands"
		}
		return fail(StatusFailed, "%s", why)
	}
	if res.Code != 0 {
		return fail(StatusRefused, "Hermes did not apply it (exit %d): %s", res.Code, lastUseful(res.Output))
	}
	if hermes.PluginUpdateRefused(res.Output) && !containsAny(res.Output, hermes.PluginCurrentTexts) {
		// exit 0 decides; the words are kept for the log
		s.logf("extras: plugin %s: exit 0, but the output has a refusal word: %s", name, tail(res.Output, 300))
	}
	if containsAny(res.Output, hermes.PluginCurrentTexts) {
		out.Status, out.Detail = StatusCurrent, "already current"
		return out
	}
	out.Status, out.Detail = StatusUpdated, "updated"

	// look: still enabled, provider unchanged
	after, aerr := s.PluginState(ctx)
	switch {
	case aerr != nil:
		out.Problems = append(out.Problems, "config.yaml could not be read afterwards ("+aerr.Error()+"), so the plugin settings were not checked: run `hermes plugins list`")
	case berr != nil:
		// no before-state: only absolute facts can be checked
		if after.IsDisabled(name) {
			out.Problems = append(out.Problems, name+" is switched off: run `hermes plugins enable "+name+"`")
		}
	default:
		g := Compare(before, after, res.Output)
		if contains(g.Evicted, name) {
			out.Problems = append(out.Problems, name+" is switched off now: run `hermes plugins enable "+name+"`")
		}
		for _, n := range g.Evicted {
			if n != name {
				out.Problems = append(out.Problems, n+" was switched off meanwhile (a plugin update never does that): run `hermes plugins enable "+n+"` if that was not you")
			}
		}
		if g.ProviderChanged {
			out.Problems = append(out.Problems, fmt.Sprintf("memory.provider changed from %q to %q: run `hermes config set memory.provider %s` if that was not you", g.Provider, g.ProviderNow, g.Provider))
		}
	}
	for _, p := range out.Problems {
		s.logf("WARN extras: plugin %s: %s", name, p)
	}
	if len(out.Problems) > 0 {
		out.Detail = "updated, but " + strings.Join(out.Problems, "; ")
	}
	return out
}

// EnableOutcome is the result of EnablePlugin and SetMemoryProvider.
type EnableOutcome struct {
	OK     bool
	Detail string
	Output string
}

// EnablePlugin runs `hermes plugins enable <name>`: Hermes re-admits the
// plugin through its dependency manager and fails atomically when the
// environment still cannot build it. OK needs exit 0 and, when config.yaml can
// be read, the plugin enabled and not disabled afterwards.
func (s *Svc) EnablePlugin(ctx context.Context, name string) EnableOutcome {
	if !hermes.SafePluginName(name) {
		return EnableOutcome{Detail: "not a plugin name this tool will pass on"}
	}
	res, err := s.atomic(ctx, EnableTimeout, hermes.PluginEnableArgv(name)...)
	s.logf("extras: plugins enable %s: exit %d (%s)", name, res.Code, tail(res.Output, 400))
	if why := failText(res, err); why != "" {
		if res.Stalled {
			why += "; run `hermes plugins enable " + name + "` in a terminal and answer it there"
		}
		return EnableOutcome{Detail: why, Output: res.Output}
	}
	if res.Code != 0 {
		return EnableOutcome{Detail: fmt.Sprintf("exit %d: %s", res.Code, lastUseful(res.Output)), Output: res.Output}
	}
	if st, err := s.PluginState(ctx); err == nil && (!st.IsEnabled(name) || st.IsDisabled(name)) {
		return EnableOutcome{Detail: "Hermes said it was enabled, but config.yaml still has it off", Output: res.Output}
	}
	return EnableOutcome{OK: true, Detail: "enabled", Output: res.Output}
}

// SetMemoryProvider runs `hermes config set memory.provider <name>`: Hermes
// writes its own config; Safe Update never edits config.yaml.
func (s *Svc) SetMemoryProvider(ctx context.Context, name string) EnableOutcome {
	if !hermes.SafePluginName(name) {
		return EnableOutcome{Detail: "not a plugin name this tool will pass on"}
	}
	res, err := s.atomic(ctx, EnableTimeout, hermes.ConfigSetProviderArgv(name)...)
	s.logf("extras: config set memory.provider %s: exit %d (%s)", name, res.Code, tail(res.Output, 300))
	if why := failText(res, err); why != "" {
		return EnableOutcome{Detail: why, Output: res.Output}
	}
	if res.Code != 0 {
		return EnableOutcome{Detail: fmt.Sprintf("exit %d: %s", res.Code, lastUseful(res.Output)), Output: res.Output}
	}
	if st, err := s.PluginState(ctx); err == nil && st.Provider != name {
		return EnableOutcome{Detail: fmt.Sprintf("Hermes said it was set, but config.yaml has memory.provider %q", st.Provider), Output: res.Output}
	}
	return EnableOutcome{OK: true, Detail: "set", Output: res.Output}
}

func containsAny(s string, subs []string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

func lastUseful(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return tail(l, 200)
		}
	}
	return "no message"
}

// ---- skills ----

// SkillsOutcome is the result of UpdateSkills.
type SkillsOutcome struct {
	OK     bool
	Detail string
	Output string
}

// UpdateSkills runs `hermes skills update`. Hermes skips skills you edited
// (it says so) and refuses a new version its scan blocks; a "Not updated"
// line or a non-zero exit is reported as such.
func (s *Svc) UpdateSkills(ctx context.Context) SkillsOutcome {
	res, err := s.atomic(ctx, SkillsApplyTmo, hermes.SkillsUpdateArgv...)
	s.logf("extras: skills update exit %d (%s)", res.Code, tail(res.Output, 400))
	if why := failText(res, err); why != "" {
		return SkillsOutcome{Detail: why, Output: res.Output}
	}
	skipped := strings.Contains(res.Output, hermes.SkillsKeptEdits)
	switch {
	case res.Code != 0 || strings.Contains(res.Output, hermes.SkillsNotUpdated):
		return SkillsOutcome{Detail: "Hermes did not update every skill: " + lastUseful(res.Output), Output: res.Output}
	case skipped:
		return SkillsOutcome{OK: true, Detail: "updated; skills you edited were kept (hermes skills update <name> --force overwrites them)", Output: res.Output}
	}
	return SkillsOutcome{OK: true, Detail: "updated", Output: res.Output}
}
