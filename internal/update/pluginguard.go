package update

// The plugin guard (v0.7.0). `hermes update`, its follow-ups and an install
// repair call the dependency manager with eviction enabled: when the rebuilt
// environment no longer builds with a plugin, Hermes switches that plugin off
// (it goes into plugins.disabled) and clears memory.provider if it was the
// provider. That is what happened on 22 Oct. A plugin update (`hermes plugins
// update`) never evicts; only the Hermes update does.
//
// So the guard brackets the Hermes update: the pre-flight reads
// plugins.enabled, plugins.disabled and memory.provider (before anything is
// closed), and once the update is over (any path that ran `hermes update`) the
// state is read again and compared. A plugin that was already disabled before
// the run, or a memory.provider the user had changed before it, is not an
// eviction. On a hit the card says so; an attended run is asked once whether
// to put it back, and does that only with Hermes' own commands:
// `hermes plugins enable <name>` (which re-admits the plugin through the
// dependency manager and fails atomically when the environment still cannot
// build it) and then `hermes config set memory.provider <old>`. Safe Update
// never edits config.yaml.

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudn8ive/hermes-safe-update/internal/config"
	"github.com/cloudn8ive/hermes-safe-update/internal/extras"
	"github.com/cloudn8ive/hermes-safe-update/internal/hermes"
	"github.com/cloudn8ive/hermes-safe-update/internal/ui"
)

// guardOn: the guard needs the service and is part of the plugins extra.
func (r *Run) guardOn() bool {
	return r.Extras != nil && r.Config.Extras.Plugins != config.ExtrasOff
}

// snapshotPlugins reads the plugin settings in the pre-flight of an update.
func (r *Run) snapshotPlugins(ctx context.Context) {
	if r.Mode != ModeUpdate || !r.guardOn() {
		return
	}
	st, err := r.Extras.PluginState(ctx)
	if err != nil {
		r.Log.Info("plugin guard: the plugin settings could not be read before the update; it will not compare", "error", err.Error())
		return
	}
	r.Facts.PluginsBefore = &st
	r.Log.Info(fmt.Sprintf("plugin guard: before the update enabled=%v disabled=%v memory.provider=%q", st.Enabled, st.Disabled, st.Provider))
}

// refreshPluginSnapshot takes the snapshot again right before the Hermes
// update when a plugin update ran earlier in this run (the questions before
// the repair offer): plugins updated there are the new baseline, so the guard
// does not flag their changes nor miss an eviction against a stale one. A read
// that fails drops the snapshot (the guard then does not compare) rather than
// keep an old one.
func (r *Run) refreshPluginSnapshot(ctx context.Context) {
	if !r.Facts.PluginUpdateRan || r.Facts.PluginsBefore == nil || r.pluginsRefreshed {
		return
	}
	r.pluginsRefreshed = true
	r.Facts.PluginsBefore = nil
	r.snapshotPlugins(ctx)
}

// guardPending: a `hermes update` ran in this run and the guard has not
// looked yet.
func (r *Run) guardPending() bool {
	return r.guardOn() && !r.guardDone && r.Facts.UpdateStarted && r.Facts.PluginsBefore != nil
}

func stageGuard(ctx context.Context, r *Run) error {
	r.runGuard(ctx, true)
	return nil
}

// runGuard compares the plugin settings with the pre-flight snapshot. mayAsk
// is false in finally (report only).
func (r *Run) runGuard(ctx context.Context, mayAsk bool) {
	if !r.guardPending() || ctx.Err() != nil && mayAsk {
		return
	}
	r.guardDone = true
	defer func() {
		if p := recover(); p != nil {
			r.warn("plugin guard: internal error: %v", p)
		}
	}()
	before := *r.Facts.PluginsBefore
	attn := ptr(ui.VerdictAttention)
	after, err := r.Extras.PluginState(ctx)
	if err != nil {
		r.warn("the plugin settings could not be read after the Hermes update (%v); check with `hermes plugins list`", err)
		r.extraRow("Plugins", "could not check whether Hermes switched a plugin off during the update ("+err.Error()+"); run hermes plugins list", attn)
		return
	}
	g := extras.Compare(before, after, r.Facts.UpdateOutput)
	for _, l := range g.Reasons {
		r.Log.Info("plugin guard: Hermes said: " + l)
	}
	if !g.Any() {
		r.Log.Info(fmt.Sprintf("plugin guard: no plugin switched off, memory.provider unchanged (enabled=%v disabled=%v memory.provider=%q)", after.Enabled, after.Disabled, after.Provider))
		return
	}

	// what happened
	var parts []string
	if len(g.Evicted) > 0 {
		s := "Hermes switched off " + strings.Join(g.Evicted, ", ") + " during the update"
		if g.ProviderEvicted() {
			s += " (memory provider cleared)"
		}
		parts = append(parts, s)
	}
	if g.ProviderChanged && !g.ProviderEvicted() {
		if g.ProviderCleared {
			parts = append(parts, fmt.Sprintf("Hermes cleared memory.provider (it was %q) during the update", g.Provider))
		} else {
			parts = append(parts, fmt.Sprintf("memory.provider changed from %q to %q during the update", g.Provider, g.ProviderNow))
		}
	}
	what := strings.Join(parts, "; ")
	r.warn("%s", what)

	// the commands that put it back
	var cmds []string
	canEnable := false
	for _, n := range g.Evicted {
		if hermes.SafePluginName(n) {
			cmds = append(cmds, "hermes plugins enable "+n)
			canEnable = true
		}
	}
	// set memory.provider only follows an enable of the very plugin that was
	// the provider (the commands Safe Update runs); otherwise it is only named
	setCmd := ""
	if g.ProviderCleared && hermes.SafePluginName(g.Provider) {
		setCmd = "hermes config set memory.provider " + g.Provider
		cmds = append(cmds, setCmd)
	}
	cmdText := "run " + strings.Join(cmds, ", then ")
	if len(cmds) == 0 {
		cmdText = "check with hermes plugins list"
	}

	if why := r.guardNoAsk(mayAsk); why != "" {
		r.extraRow("Plugins", what+"; not put back ("+why+"): "+cmdText, ptr(ui.VerdictBad))
		return
	}
	if !canEnable {
		// nothing was switched off, so there is no plugin to re-enable; the
		// provider setting is the user's to decide
		r.extraRow("Plugins", what+"; not put back (Hermes did not switch a plugin off): "+cmdText, ptr(ui.VerdictBad))
		return
	}
	runs := cmds
	if setCmd != "" && !g.ProviderEvicted() {
		runs = cmds[:len(cmds)-1] // the provider is not set unless its own plugin was switched off and comes back
	}
	q := fmt.Sprintf("%s. Put it back? Safe Update runs %s. Hermes rebuilds the plugin's environment and refuses if it still cannot build it; nothing is changed then. Press Y to put it back, any other key to leave it.",
		what, strings.Join(backticked(runs), ", then "))
	if !r.extrasYes(ctx, q, "putting the plugins back") {
		r.extraRow("Plugins", what+"; not put back (you chose to leave it): "+cmdText, ptr(ui.VerdictBad))
		return
	}

	// put it back, with Hermes' own commands
	enabled := map[string]bool{}
	var done, failed []string
	for _, n := range g.Evicted {
		if !hermes.SafePluginName(n) {
			continue
		}
		r.info("Re-enabling plugin %s ...", n)
		stop := r.finishingNote(ctx, "putting plugin "+n+" back", extras.EnableTimeout)
		o := r.Extras.EnablePlugin(ctx, n)
		stop()
		r.Log.Info("plugin guard: enable", "name", n, "ok", o.OK, "detail", o.Detail)
		if o.OK {
			enabled[n] = true
			done = append(done, n+" enabled")
		} else {
			failed = append(failed, n+": "+o.Detail)
			r.warn("could not put %s back: %s", n, o.Detail)
		}
	}
	if g.ProviderCleared && hermes.SafePluginName(g.Provider) {
		if enabled[g.Provider] {
			o := r.Extras.SetMemoryProvider(ctx, g.Provider)
			r.Log.Info("plugin guard: config set memory.provider", "name", g.Provider, "ok", o.OK, "detail", o.Detail)
			if o.OK {
				done = append(done, "memory.provider set to "+g.Provider)
			} else {
				failed = append(failed, "memory.provider: "+o.Detail)
				r.warn("could not set memory.provider back to %s: %s", g.Provider, o.Detail)
			}
		} else if hasName(g.Evicted, g.Provider) {
			failed = append(failed, "memory.provider left cleared because "+g.Provider+" could not be enabled")
		} else {
			failed = append(failed, "memory.provider left cleared (Hermes did not switch "+g.Provider+" off); to set it: "+setCmd)
		}
	}
	if len(failed) > 0 {
		r.extraRow("Plugins", what+"; NOT fully put back: "+strings.Join(failed, "; ")+" (still to do: "+cmdText+")", ptr(ui.VerdictBad))
		return
	}
	r.extraRow("Plugins", what+"; put back: "+strings.Join(done, ", "), ptr(ui.VerdictGood))
}

func hasName(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func backticked(cmds []string) []string {
	out := make([]string, len(cmds))
	for i, c := range cmds {
		out[i] = "`" + c + "`"
	}
	return out
}

// guardNoAsk says why the guard only reports ("" = it may ask): finally, an
// unverified Hermes, nobody to ask, the extras.plugins setting, a chat.
func (r *Run) guardNoAsk(mayAsk bool) string {
	switch {
	case !mayAsk:
		return "the run ended early"
	case r.applyBlock() != "":
		return r.applyBlock()
	case r.quiet():
		return "no one to ask"
	case r.Config.Extras.Plugins != config.ExtrasAsk:
		return "extras.plugins is " + r.Config.Extras.Plugins
	case !r.extrasCanAsk(context.Background()):
		return "this is not an attended terminal run"
	}
	return ""
}
