// Package extras looks after the parts of a Hermes install that do not depend
// on the Hermes version: catalog plugins, hub-installed skills and the
// bundled tools (git, node, ...). It probes them read-only and in parallel,
// and (only when the caller asked and the user said yes) updates one plugin
// at a time through Hermes (and looks at the result), or the hub skills.
//
// It never touches Hermes' own checkout and never runs `hermes pm update`:
// that command rewrites core files, so tool versions are listed only.
package extras

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/cloudn8ive/hermes-safe-update/internal/execx"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
)

// Time boxes of the probes and of the applies. A probe that runs out reports
// "timed out"; the run goes on.
const (
	PluginsTimeout  = 90 * time.Second
	SkillsTimeout   = 90 * time.Second
	DoctorTimeout   = 150 * time.Second
	PMUpdateTimeout = 120 * time.Second
	// ApplyTimeout bounds one plugin update (a dependency rebuild can take minutes).
	ApplyTimeout = 15 * time.Minute
	// PromptQuiet: a Hermes apply command that printed a [y/N] question and
	// then nothing for this long is cut (nobody can answer it; ApplyTimeout stays the hard cap).
	PromptQuiet    = 60 * time.Second
	EnableTimeout  = 15 * time.Minute
	SkillsApplyTmo = 10 * time.Minute
)

var utf8Env = map[string]string{"PYTHONIOENCODING": "utf-8", "PYTHONUTF8": "1", "COLUMNS": "200"}

// Deps are the collaborators of the service.
type Deps struct {
	Runner   execx.Runner
	Launcher string // the `hermes` launcher
	Home     string // the Hermes home (config.yaml, plugins/)
	Now      func() time.Time
	Log      *slog.Logger
	// KillTree stops a Hermes child and everything it started (the launcher
	// hands the work to a Python child, which a plain kill would leave
	// running). Set on every command; production passes the platform's
	// process-tree stop.
	KillTree func(pid int) error
	// Wait pauses between two reads of config.yaml (default time.Sleep).
	Wait func(time.Duration)
	// Git and Checkout serve the stale-release row only.
	Git, Checkout string
}

// Service is what internal/update uses; Svc is the real one.
type Service interface {
	// Probe runs the asked read-only probes side by side.
	Probe(ctx context.Context, o Options) Report
	// UpdatePlugin updates one plugin through Hermes and checks the result.
	UpdatePlugin(ctx context.Context, name string) PluginOutcome
	// PluginState reads plugins.enabled, plugins.disabled and memory.provider.
	PluginState(ctx context.Context) (State, error)
	// EnablePlugin runs `hermes plugins enable <name>`.
	EnablePlugin(ctx context.Context, name string) EnableOutcome
	// SetMemoryProvider runs `hermes config set memory.provider <name>`.
	SetMemoryProvider(ctx context.Context, name string) EnableOutcome
	// UpdateSkills runs `hermes skills update`.
	UpdateSkills(ctx context.Context) SkillsOutcome
	// CheckStale reads the release age and the security-looking commits since.
	CheckStale(ctx context.Context, p StaleParams) (Stale, bool)
}

var _ Service = (*Svc)(nil)

// Svc is the real service.
type Svc struct{ d Deps }

// New returns the service.
func New(d Deps) *Svc {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Wait == nil {
		d.Wait = time.Sleep
	}
	if d.Log == nil {
		d.Log = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	return &Svc{d: d}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// Options says what to probe.
type Options struct {
	Plugins  bool
	Skills   bool
	Tools    bool // pm doctor
	PMUpdate bool // pm update --check (slow, network): check mode only
}

// PluginsResult is the probe of `hermes plugins check-updates --json`.
type PluginsResult struct {
	Rows []hermes.PluginRow
	Err  string // "" = read; else why not (timed out, unreadable, rc)
	// Pins is what the install record says about every row that has an
	// update Safe Update could apply (see hermes.PinStore). A row without
	// an entry was not looked at.
	Pins map[string]hermes.PluginPin
}

// pinned reports the verdict for a row (the zero value: not held).
func (p PluginsResult) pinned(name string) hermes.PluginPin { return p.Pins[name] }

// Updatable are the rows an update can be applied to: Hermes has an update
// path, an update waits, and the install record says the plugin is not held
// (--ref) nor unreadable.
func (p PluginsResult) Updatable() []hermes.PluginRow {
	var out []hermes.PluginRow
	for _, r := range p.Rows {
		if r.Updatable() && !p.pinned(r.Name).Held() {
			out = append(out, r)
		}
	}
	return out
}

// Held are the rows that have an update which Safe Update will not apply
// because the plugin is pinned with --ref (or its record cannot be read).
func (p PluginsResult) Held() []HeldRow {
	var out []HeldRow
	for _, r := range p.Rows {
		if pin := p.pinned(r.Name); r.Updatable() && pin.Held() {
			out = append(out, HeldRow{Row: r, Pin: pin})
		}
	}
	return out
}

// HeldRow is a plugin with an update that is not applied, and why.
type HeldRow struct {
	Row hermes.PluginRow
	Pin hermes.PluginPin
}

// Other are rows that need attention but cannot be updated by Safe Update:
// an update exists for a class Hermes does not move, or the plugin "needs
// fixing". A row listed by Held is not repeated here.
func (p PluginsResult) Other() []hermes.PluginRow {
	var out []hermes.PluginRow
	for _, r := range p.Rows {
		if r.Updatable() {
			continue
		}
		if r.NeedsFixing != nil || (r.Update != nil && *r.Update) {
			out = append(out, r)
		}
	}
	return out
}

// SkillsResult is the probe of `hermes skills check`.
type SkillsResult struct {
	Check hermes.SkillCheck
	Err   string
}

// ToolsResult is the probe of `hermes pm doctor` and, in check mode, of
// `hermes pm update --check`.
type ToolsResult struct {
	Doctor      hermes.DoctorReport
	DoctorErr   string // "" = read
	Updates     []hermes.ToolUpdate
	UpdatesRan  bool
	UpdatesErr  string
	DoctorRaw   string
	UpdatesRaw  string
	DoctorRanOK bool
}

// Report is everything one probe pass learned. A nil part was not asked for.
type Report struct {
	Plugins *PluginsResult
	Skills  *SkillsResult
	Tools   *ToolsResult
}

func (s *Svc) logf(format string, a ...any) { s.d.Log.Info(fmt.Sprintf(format, a...)) }

func (s *Svc) hermes(ctx context.Context, tmo time.Duration, tail ...string) (execx.Result, error) {
	return s.hermesCmd(ctx, execx.Cmd{Timeout: tmo}, tail...)
}

// hermesCmd runs `hermes <tail>` with the options of c (Timeout, PromptQuiet).
func (s *Svc) hermesCmd(ctx context.Context, c execx.Cmd, tail ...string) (execx.Result, error) {
	c.Argv, c.Env, c.KillTree = append([]string{s.d.Launcher}, tail...), utf8Env, s.d.KillTree
	return s.d.Runner.Run(ctx, c)
}

// failText says why a command's output cannot be used, "" when it ran fine.
func failText(res execx.Result, err error) string {
	switch {
	case err != nil:
		return err.Error()
	case res.Stalled:
		return fmt.Sprintf("Hermes asked a question that needs a terminal (%q) and it was cut after %s of silence", res.Prompt, PromptQuiet)
	case res.TimedOut:
		return "timed out"
	case res.Code == execx.CodeNotFound:
		return "could not start: " + tail(res.Output, 120)
	}
	return ""
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = "..." + s[len(s)-n:]
	}
	return strings.ReplaceAll(s, "\n", " ")
}

// Probe runs the asked probes side by side.
func (s *Svc) Probe(ctx context.Context, o Options) Report {
	var rep Report
	var wg sync.WaitGroup
	if o.Plugins {
		rep.Plugins = &PluginsResult{}
		wg.Add(1)
		go func() { defer wg.Done(); *rep.Plugins = s.probePlugins(ctx) }()
	}
	if o.Skills {
		rep.Skills = &SkillsResult{}
		wg.Add(1)
		go func() { defer wg.Done(); *rep.Skills = s.probeSkills(ctx) }()
	}
	if o.Tools {
		rep.Tools = &ToolsResult{}
		wg.Add(1)
		go func() { defer wg.Done(); s.probeDoctor(ctx, rep.Tools) }()
		if o.PMUpdate {
			wg.Add(1)
			go func() { defer wg.Done(); s.probePMUpdate(ctx, rep.Tools) }()
		}
	}
	wg.Wait()
	if rep.Tools != nil {
		s.onlyInstalledTools(rep.Tools)
	}
	return rep
}

func (s *Svc) probePlugins(ctx context.Context) PluginsResult {
	res, err := s.hermes(ctx, PluginsTimeout, hermes.PluginsCheckArgv...)
	if why := failText(res, err); why != "" {
		s.logf("extras: plugins check-updates: %s", why)
		return PluginsResult{Err: why}
	}
	if res.Code != 0 {
		s.logf("extras: plugins check-updates exit %d: %s", res.Code, tail(res.Output, 300))
		return PluginsResult{Err: fmt.Sprintf("exit %d (this Hermes may not have `plugins check-updates --json`)", res.Code)}
	}
	rows, perr := hermes.ParsePluginRows(res.Output)
	if perr != nil {
		s.logf("WARN Hermes output changed: plugins check-updates --json: %v", perr)
		return PluginsResult{Err: "could not read Hermes' answer"}
	}
	for _, r := range rows {
		s.logf("extras: plugin %s class=%s current=%s latest=%s update=%v reason=%q", r.Name, r.Class,
			hermes.Short(r.Current), hermes.Short(r.Latest), r.Update != nil && *r.Update, r.Reason)
	}
	// Which of the rows with an update are held with --ref: the check does
	// not say, the install record does (read-only; see hermes/pins.go).
	var pins map[string]hermes.PluginPin
	var store *hermes.PinStore
	for _, r := range rows {
		if !r.Updatable() {
			continue
		}
		if store == nil {
			st := hermes.ReadPinStore(s.d.Home)
			store, pins = &st, map[string]hermes.PluginPin{}
		}
		pin := store.Pin(r.Name, r.Class)
		pins[r.Name] = pin
		switch pin.Kind {
		case hermes.PinRef, hermes.PinGit:
			s.logf("extras: plugin %s is pinned (%s) to %s: not offered", r.Name, pinKindText(pin.Kind), hermes.ShortRef(pin.Ref))
		case hermes.PinUnknown:
			s.logf("extras: plugin %s: pin state unknown (%s): not offered", r.Name, pin.Why)
		}
	}
	return PluginsResult{Rows: rows, Pins: pins}
}

func pinKindText(k hermes.PinKind) string {
	if k == hermes.PinGit {
		return "git install with a fixed revision"
	}
	return "--ref"
}

func (s *Svc) probeSkills(ctx context.Context) SkillsResult {
	res, err := s.hermes(ctx, SkillsTimeout, hermes.SkillsCheckArgv...)
	if why := failText(res, err); why != "" {
		s.logf("extras: skills check: %s", why)
		return SkillsResult{Err: why}
	}
	if res.Code != 0 {
		s.logf("extras: skills check exit %d: %s", res.Code, tail(res.Output, 300))
		return SkillsResult{Err: fmt.Sprintf("exit %d", res.Code)}
	}
	sc, ok := hermes.ParseSkillCheck(res.Output)
	if !ok {
		s.logf("WARN Hermes output changed: skills check: no count line and no \"no skills\" line")
		return SkillsResult{Err: "could not read Hermes' answer"}
	}
	for _, r := range sc.Rows {
		s.logf("extras: skill %s source=%s status=%s", r.Name, r.Source, r.Status)
	}
	return SkillsResult{Check: sc}
}

func (s *Svc) probeDoctor(ctx context.Context, t *ToolsResult) {
	res, err := s.hermes(ctx, DoctorTimeout, hermes.PMDoctorArgv...)
	if why := failText(res, err); why != "" {
		t.DoctorErr = why
		s.logf("extras: pm doctor: %s", why)
		return
	}
	t.DoctorRaw = res.Output
	d, ok := hermes.ParseDoctor(res.Output)
	if !ok {
		t.DoctorErr = "could not read Hermes' answer"
		s.logf("WARN Hermes output changed: pm doctor: no line understood (exit %d): %s", res.Code, tail(res.Output, 200))
		return
	}
	if res.Code != 0 && len(d.Faults) == 0 {
		// non-zero without a fault line: do not call that healthy
		t.DoctorErr = fmt.Sprintf("exit %d without a fault line", res.Code)
		s.logf("WARN pm doctor exit %d but no fault line was found", res.Code)
		return
	}
	t.Doctor, t.DoctorRanOK = d, true
	for _, f := range d.Faults {
		s.logf("extras: pm doctor fault: %s: %s (%s)", f.Name, f.Reason, f.Advice())
	}
	s.logf("extras: pm doctor: %d ok, %d optional missing, %d n/a, %d fault(s)", d.OK, d.Soft, d.NA, len(d.Faults))
}

func (s *Svc) probePMUpdate(ctx context.Context, t *ToolsResult) {
	t.UpdatesRan = true
	res, err := s.hermes(ctx, PMUpdateTimeout, hermes.PMUpdateCheckArgv...)
	if why := failText(res, err); why != "" {
		t.UpdatesErr = why
		s.logf("extras: pm update --check: %s", why)
		return
	}
	t.UpdatesRaw = res.Output
	t.Updates = hermes.ParsePMUpdateCheck(res.Output)
	if len(t.Updates) == 0 && res.Code != 0 {
		// exit 1 means "updates exist" and 504s hide them; with no parsed line nothing is known
		t.UpdatesErr = fmt.Sprintf("exit %d and no version line", res.Code)
	}
}

// onlyInstalledTools drops the version rows of tools that are not installed
// here, once both tool probes are done (they run side by side).
func (s *Svc) onlyInstalledTools(t *ToolsResult) {
	if t.DoctorRanOK {
		// only tools installed on this PC: pm lists the optional ones too
		// (the llama.cpp builds, for one) whether or not they were installed
		var here []hermes.ToolUpdate
		for _, u := range t.Updates {
			if t.Doctor.Present(u.Name) {
				here = append(here, u)
			} else {
				s.logf("extras: tool update not shown (not installed here): %s %s -> %s", u.Name, u.From, u.To)
			}
		}
		t.Updates = here
	}
	for _, u := range t.Updates {
		s.logf("extras: tool update (listing only): %s %s -> %s", u.Name, u.From, u.To)
	}
}

// pinHeldText says why a plugin is not updated (log and outcome detail).
func pinHeldText(p hermes.PluginPin) string {
	switch p.Kind {
	case hermes.PinRef:
		return "pinned with --ref to " + hermes.ShortRef(p.Ref) + "; `hermes plugins update <name>` would move it off the pin"
	case hermes.PinGit:
		return "pinned to " + hermes.ShortRef(p.Ref) + "; Hermes will not update a pinned plugin (reinstall it with --ref to move it)"
	}
	return "its pin state is unknown (" + p.Why + ")"
}

// HeldText is the row text for one held plugin.
func (h HeldRow) HeldText() string {
	switch h.Pin.Kind {
	case hermes.PinRef:
		return h.Row.Name + " pinned to " + hermes.ShortRef(h.Pin.Ref) + " (not updated; hermes plugins update " + h.Row.Name + " moves it off the pin)"
	case hermes.PinGit:
		return h.Row.Name + " pinned to " + hermes.ShortRef(h.Pin.Ref) + " (not updated; Hermes will not update a pinned plugin: reinstall it with --ref to move it)"
	}
	return h.Row.Name + " has an update, but its pin state is unknown (" + h.Pin.Why + "), so it was not touched"
}
